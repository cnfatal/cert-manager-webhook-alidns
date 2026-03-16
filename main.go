package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	alidns "github.com/alibabacloud-go/alidns-20150109/v5/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/cert-manager/cert-manager/pkg/acme/webhook/cmd"
	cmmetav1 "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	logf "github.com/cert-manager/cert-manager/pkg/logs"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var ErrNotFound = errors.New("record not found")

var log = logf.Log.WithName("alidns-solver")

var GroupName = os.Getenv("GROUP_NAME")

func main() {
	if GroupName == "" {
		panic("GROUP_NAME must be specified")
	}

	cmd.RunWebhookServer(GroupName,
		&aliDNSProviderSolver{},
	)
}

type aliDNSProviderSolver struct {
	client kubernetes.Interface
}

type aliDNSProviderConfig struct {
	Email           string                     `json:"email"`
	APIKeySecretRef cmmetav1.SecretKeySelector `json:"apiKeySecretRef"`
	AccessKeyID     string                     `json:"accessKeyID"`
	AccessKeySecret string                     `json:"accessKeySecret"`
	RegionID        string                     `json:"regionID"`
}

func (c *aliDNSProviderSolver) Name() string {
	return "alidns-solver"
}

func (c *aliDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	ch.Action = v1alpha1.ChallengeActionPresent
	return c.Reconcile(ch)
}

func (c *aliDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	ch.Action = v1alpha1.ChallengeActionCleanUp
	return c.Reconcile(ch)
}

func (c *aliDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return err
	}
	c.client = cl
	return nil
}

func (c *aliDNSProviderSolver) loadConfig(ch *v1alpha1.ChallengeRequest) (aliDNSProviderConfig, error) {
	cfg := aliDNSProviderConfig{}
	if ch.Config == nil {
		return cfg, nil
	}
	if err := json.Unmarshal(ch.Config.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %v", err)
	}

	if cfg.APIKeySecretRef.Name != "" {
		namespace := ch.ResourceNamespace
		secretName := cfg.APIKeySecretRef.Name

		log.Info("loading config from secret", "secret", secretName, "namespace", namespace)
		secret, err := c.client.CoreV1().Secrets(namespace).Get(context.TODO(), secretName, metav1.GetOptions{})
		if err != nil {
			return cfg, fmt.Errorf("failed to load secret %s: %w", namespace+"/"+secretName, err)
		}

		accessKeys := []string{"access-key", "accessKeyID"}
		secretKeys := []string{"secret-key", "accessKeySecret"}

		for _, k := range accessKeys {
			if v, ok := secret.Data[k]; ok {
				cfg.AccessKeyID = string(v)
				break
			}
		}
		for _, k := range secretKeys {
			if v, ok := secret.Data[k]; ok {
				cfg.AccessKeySecret = string(v)
				break
			}
		}
	}
	return cfg, nil
}

func (c *aliDNSProviderSolver) Reconcile(ch *v1alpha1.ChallengeRequest) error {
	log := log.WithValues("request", ch)
	log.Info("start reconcile")

	cfg, err := c.loadConfig(ch)
	if err != nil {
		log.Error(err, "failed to load config")
		return err
	}

	if cfg.AccessKeySecret == "" || cfg.AccessKeyID == "" {
		err = fmt.Errorf("accessKeySecret or accessKeyID is empty")
		log.Error(err, "invalid config")
		return err
	}

	config := &openapi.Config{
		AccessKeyId:     tea.String(cfg.AccessKeyID),
		AccessKeySecret: tea.String(cfg.AccessKeySecret),
	}
	if cfg.RegionID != "" {
		config.RegionId = tea.String(cfg.RegionID)
	}
	client, err := alidns.NewClient(config)
	if err != nil {
		return err
	}

	domain := baseDomain(unFqdn(ch.ResolvedZone))
	rr := strings.TrimSuffix(ch.ResolvedFQDN, "."+domain+".")
	typ := "TXT"

	switch ch.Action {
	case v1alpha1.ChallengeActionPresent:
		return createOrUpdateRecord(client, typ, rr, domain, ch.Key)
	case v1alpha1.ChallengeActionCleanUp:
		return removeRecord(client, typ, rr, domain)
	default:
		return fmt.Errorf("unsupported challenge action: %s", ch.Action)
	}
}

// unFqdn removes the trailing dot from a fully qualified domain name.
func unFqdn(name string) string {
	return strings.TrimSuffix(name, ".")
}

func baseDomain(domain string) string {
	splites := strings.Split(domain, ".")
	if lens := len(splites); lens > 2 {
		return splites[lens-2] + "." + splites[lens-1]
	}
	return domain
}

func createOrUpdateRecord(client *alidns.Client, typ, rr, domain, val string) error {
	log.Info("record creating/updating", "type", typ, "rr", rr, "domain", domain, "val", val)

	record, err := getRecord(client, typ, rr, domain)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		return createRecord(client, typ, rr, domain, val)
	}
	if tea.StringValue(record.Value) != val || tea.StringValue(record.Type) != typ {
		return updateRecord(client, tea.StringValue(record.RecordId), typ, rr, val)
	}
	log.Info("record already updated")
	return nil
}

func removeRecord(client *alidns.Client, typ, rr, domain string) error {
	log := log.WithValues("type", typ, "rr", rr, "domain", domain)
	log.Info("record removing")

	record, err := getRecord(client, typ, rr, domain)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			log.Info("record not found ignored")
			return nil
		}
		log.Error(err, "record remove failed")
		return err
	}
	req := &alidns.DeleteDomainRecordRequest{}
	req.SetRecordId(tea.StringValue(record.RecordId))
	if _, err := client.DeleteDomainRecord(req); err != nil {
		return err
	}
	log.Info("record removed")
	return nil
}

func createRecord(cli *alidns.Client, typ, rr, domain string, val string) error {
	log := log.WithValues("type", typ, "rr", rr, "domain", domain, "val", val)
	log.Info("record creating")

	req := &alidns.AddDomainRecordRequest{}
	req.SetDomainName(domain).SetRR(rr).SetType(typ).SetValue(val)
	if _, err := cli.AddDomainRecord(req); err != nil {
		log.Error(err, "record create failed")
		return err
	}
	log.Info("record created")
	return nil
}

func updateRecord(cli *alidns.Client, recordID, typ, rr, val string) error {
	log := log.WithValues("type", typ, "recordID", recordID, "rr", rr, "val", val)
	log.Info("record updating")

	req := &alidns.UpdateDomainRecordRequest{}
	req.SetRecordId(recordID).SetRR(rr).SetType(typ).SetValue(val)
	if _, err := cli.UpdateDomainRecord(req); err != nil {
		log.Error(err, "record update failed")
		return err
	}
	log.Info("record updated")
	return nil
}

func getRecord(cli *alidns.Client, typ, rr, domain string) (*alidns.DescribeDomainRecordsResponseBodyDomainRecordsRecord, error) {
	records, err := listRecords(cli, typ, rr, domain)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if tea.StringValue(record.RR) == rr {
			return record, nil
		}
	}
	return nil, ErrNotFound
}

func listRecords(cli *alidns.Client, typ, rr, domain string) ([]*alidns.DescribeDomainRecordsResponseBodyDomainRecordsRecord, error) {
	log := log.WithValues("type", typ, "rr", rr, "domain", domain)
	log.Info("record listing")

	records := []*alidns.DescribeDomainRecordsResponseBodyDomainRecordsRecord{}

	page := int64(1)
	for {
		req := &alidns.DescribeDomainRecordsRequest{}
		req.SetDomainName(domain).SetPageSize(100).SetSearchMode("EXACT").SetKeyWord(rr).SetType(typ).SetPageNumber(page)
		resp, err := cli.DescribeDomainRecords(req)
		if err != nil {
			log.Error(err, "record list failed")
			return nil, err
		}
		if resp.Body == nil || resp.Body.DomainRecords == nil || len(resp.Body.DomainRecords.Record) == 0 {
			break
		}
		records = append(records, resp.Body.DomainRecords.Record...)
		page++
	}
	log.Info("record listed", "count", len(records))
	return records, nil
}
