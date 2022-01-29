package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/alidns"
	"github.com/jetstack/cert-manager/pkg/acme/webhook/apis/acme/v1alpha1"
	"github.com/jetstack/cert-manager/pkg/acme/webhook/cmd"
	cmmetav1 "github.com/jetstack/cert-manager/pkg/apis/meta/v1"
	"github.com/jetstack/cert-manager/pkg/issuer/acme/dns/util"
	logf "github.com/jetstack/cert-manager/pkg/logs"
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

	// This will register our custom DNS provider with the webhook serving
	// library, making it available as an API under the provided GroupName.
	// You can register multiple DNS provider implementations with a single
	// webhook, where the Name() method will be used to disambiguate between
	// the different implementations.
	cmd.RunWebhookServer(GroupName,
		&aliDNSProviderSolver{},
	)
}

// aliDNSProviderSolver implements the provider-specific logic needed to
// 'present' an ACME challenge TXT record for your own DNS provider.
// To do so, it must implement the `github.com/jetstack/cert-manager/pkg/acme/webhook.Solver`
// interface.
type aliDNSProviderSolver struct { // If a Kubernetes 'clientset' is needed, you must:
	// 1. uncomment the additional `client` field in this structure below
	// 2. uncomment the "k8s.io/client-go/kubernetes" import at the top of the file
	// 3. uncomment the relevant code in the Initialize method below
	// 4. ensure your webhook's service account has the required RBAC role
	//    assigned to it for interacting with the Kubernetes APIs you need.
	client kubernetes.Interface
}

// aliDNSProviderConfig is a structure that is used to decode into when
// solving a DNS01 challenge.
// This information is provided by cert-manager, and may be a reference to
// additional configuration that's needed to solve the challenge for this
// particular certificate or issuer.
// This typically includes references to Secret resources containing DNS
// provider credentials, in cases where a 'multi-tenant' DNS solver is being
// created.
// If you do *not* require per-issuer or per-certificate configuration to be
// provided to your webhook, you can skip decoding altogether in favour of
// using CLI flags or similar to provide configuration.
// You should not include sensitive information here. If credentials need to
// be used by your provider here, you should reference a Kubernetes Secret
// resource and fetch these credentials using a Kubernetes clientset.
type aliDNSProviderConfig struct { // Change the two fields below according to the format of the configuration
	// to be decoded.
	// These fields will be set by users in the
	// `issuer.spec.acme.dns01.providers.webhook.config` field.

	Email           string                     `json:"email"`
	APIKeySecretRef cmmetav1.SecretKeySelector `json:"apiKeySecretRef"`
	AccessKeyID     string                     `json:"accessKeyID"`
	AccessKeySecret string                     `json:"accessKeySecret"`
	RegionID        string                     `json:"regionID"`
}

// Name is used as the name for this DNS solver when referencing it on the ACME
// Issuer resource.
// This should be unique **within the group name**, i.e. you can have two
// solvers configured with the same Name() **so long as they do not co-exist
// within a single webhook deployment**.
// For example, `cloudflare` may be used as the name of a solver.
func (c *aliDNSProviderSolver) Name() string {
	return "alidns-solver"
}

// Present is responsible for actually presenting the DNS record with the
// DNS provider.
// This method should tolerate being called multiple times with the same value.
// cert-manager itself will later perform a self check to ensure that the
// solver has correctly configured the DNS provider.
func (c *aliDNSProviderSolver) Present(ch *v1alpha1.ChallengeRequest) error {
	ch.Action = v1alpha1.ChallengeActionPresent
	return c.Reconcile(ch)
}

// CleanUp should delete the relevant TXT record from the DNS provider console.
// If multiple TXT records exist with the same record name (e.g.
// _acme-challenge.example.com) then **only** the record with the same `key`
// value provided on the ChallengeRequest should be cleaned up.
// This is in order to facilitate multiple DNS validations for the same domain
// concurrently.
func (c *aliDNSProviderSolver) CleanUp(ch *v1alpha1.ChallengeRequest) error {
	ch.Action = v1alpha1.ChallengeActionCleanUp
	return c.Reconcile(ch)
}

// Initialize will be called when the webhook first starts.
// This method can be used to instantiate the webhook, i.e. initialising
// connections or warming up caches.
// Typically, the kubeClientConfig parameter is used to build a Kubernetes
// client that can be used to fetch resources from the Kubernetes API, e.g.
// Secret resources containing credentials used to authenticate with DNS
// provider accounts.
// The stopCh can be used to handle early termination of the webhook, in cases
// where a SIGTERM or similar signal is sent to the webhook process.
func (c *aliDNSProviderSolver) Initialize(kubeClientConfig *rest.Config, stopCh <-chan struct{}) error {
	///// UNCOMMENT THE BELOW CODE TO MAKE A KUBERNETES CLIENTSET AVAILABLE TO
	///// YOUR CUSTOM DNS PROVIDER

	cl, err := kubernetes.NewForConfig(kubeClientConfig)
	if err != nil {
		return err
	}

	c.client = cl

	///// END OF CODE TO MAKE KUBERNETES CLIENTSET AVAILABLE
	return nil
}

// loadConfig is a small helper function that decodes JSON configuration into
// the typed config struct.
func (c *aliDNSProviderSolver) loadConfig(ch *v1alpha1.ChallengeRequest) (aliDNSProviderConfig, error) {
	cfg := aliDNSProviderConfig{}
	// handle the 'base case' where no configuration has been provided
	if ch.Config == nil {
		return cfg, nil
	}
	if err := json.Unmarshal(ch.Config.Raw, &cfg); err != nil {
		return cfg, fmt.Errorf("error decoding solver config: %v", err)
	}

	// try fill from secret if defined
	if cfg.APIKeySecretRef.Name != "" {
		namespace := ch.ResourceNamespace
		secretName := cfg.APIKeySecretRef.Name

		log.Info("loading config from secret", "secret", secretName, "namespace", namespace)
		secret, err := c.client.CoreV1().Secrets(namespace).Get(context.TODO(), secretName, metav1.GetOptions{})
		if err != nil {
			return cfg, fmt.Errorf("failed to load secret %s: %w", namespace+"/"+secretName, err)
		}

		accessKeys := []string{
			"access-key",
			"accessKeyID",
		}
		secretKeys := []string{
			"secret-key",
			"accessKeySecret",
		}

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

	// check configuration
	if cfg.AccessKeySecret == "" || cfg.AccessKeyID == "" {
		err = fmt.Errorf("accessKeySecret or accessKeyID is empty")
		log.Error(err, "invalid config")
		return err
	}

	client, err := alidns.NewClientWithAccessKey(cfg.RegionID, cfg.AccessKeyID, cfg.AccessKeySecret)
	if err != nil {
		return err
	}

	rr := strings.TrimSuffix(ch.ResolvedFQDN, "."+ch.ResolvedZone)
	domain := util.UnFqdn(ch.ResolvedZone)
	typ := "TXT" // ACME DNS-01 is always TXT

	switch ch.Action {
	case v1alpha1.ChallengeActionPresent:
		return createOrUpdateRecord(client, typ, rr, domain, ch.Key)
	case v1alpha1.ChallengeActionCleanUp:
		return removeRecord(client, typ, rr, domain)
	default:
		return fmt.Errorf("unsupported challenge action: %s", ch.Action)
	}
}

func createOrUpdateRecord(client *alidns.Client, typ, rr, domain, val string) error {
	log.Info("record creating/updating", "type", typ, "rr", rr, "domain", domain, "val", val)

	record, err := getRecord(client, typ, rr, domain)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		// is not found
		return createRecord(client, typ, rr, domain, val)
	}
	// need update record
	if record.Value != val || record.Type != typ {
		return updateRecord(client, record.RecordId, typ, rr, val)
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
			// not found ignore
			log.Info("record not found ignored")
			return nil
		}
		log.Error(err, "record remove failed")
		return err
	}
	// found, remove the record
	req := alidns.CreateDeleteDomainRecordRequest()
	req.RecordId = record.RecordId
	if _, err := client.DeleteDomainRecord(req); err != nil {
		return err
	}
	log.Info("record removed")
	return nil
}

func createRecord(cli *alidns.Client, typ, rr, domain string, val string) error {
	log := log.WithValues("type", typ, "rr", rr, "domain", domain, "val", val)
	log.Info("record creating")

	req := alidns.CreateAddDomainRecordRequest()
	req.Type = typ
	req.DomainName = domain
	req.RR = rr
	req.Value = val
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

	req := alidns.CreateUpdateDomainRecordRequest()
	req.Type = typ
	req.RecordId = recordID
	req.RR = rr
	req.Value = val
	if _, err := cli.UpdateDomainRecord(req); err != nil {
		log.Error(err, "record update failed")
		return err
	}
	log.Info("record updated")
	return nil
}

func getRecord(cli *alidns.Client, typ, rr, domain string) (*alidns.Record, error) {
	records, err := listRecords(cli, typ, rr, domain)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.RR == rr {
			return &record, nil
		}
	}
	return nil, ErrNotFound
}

func listRecords(cli *alidns.Client, typ, rr, domain string) ([]alidns.Record, error) {
	log := log.WithValues("type", typ, "rr", rr, "domain", domain)
	log.Info("record listing")

	req := alidns.CreateDescribeDomainRecordsRequest()
	req.DomainName = domain
	req.PageSize = requests.NewInteger(100)
	req.SearchMode = "EXACT"
	req.KeyWord = rr
	req.Type = typ

	records := []alidns.Record{}

	page := 1
	for {
		req.PageNumber = requests.NewInteger(page)
		resp, err := cli.DescribeDomainRecords(req)
		if err != nil {
			log.Error(err, "record list failed")
			return nil, err
		}
		if len(resp.DomainRecords.Record) == 0 {
			break
		}
		for _, record := range resp.DomainRecords.Record {
			records = append(records, record)
		}
		page++
	}
	log.Info("record listed", "count", len(records))
	return records, nil
}
