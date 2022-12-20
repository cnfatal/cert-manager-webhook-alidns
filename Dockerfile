FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY /bin/webhook /usr/local/bin/webhook
ENTRYPOINT ["webhook"]
