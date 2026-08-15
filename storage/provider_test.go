package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

type storageProviderSequence struct {
	credentials []Credential
	calls       int
	err         error
}

func (p *storageProviderSequence) Credential(context.Context) (Credential, error) {
	if p.err != nil {
		return Credential{}, p.err
	}
	i := p.calls
	if i >= len(p.credentials) {
		i = len(p.credentials) - 1
	}
	p.calls++
	return p.credentials[i], nil
}

func storageTestCredential(token string) Credential {
	return Credential{Bucket: "bucket", Region: "ap-northeast-1", Prefix: "apps/a/m/", CDNBase: "https://cdn.example", AccessKeyID: "key", SecretAccessKey: token, ExpiresAt: time.Now().Add(time.Hour)}
}

func TestAWSProviderRotatesAndPinsStorageScope(t *testing.T) {
	base := storageTestCredential("one")
	rotated := storageTestCredential("two")
	p := &storageProviderSequence{credentials: []Credential{base, rotated}}
	awsProvider := &awsRenewableProvider{provider: p, scope: scopeFromCredential(base)}
	first, err := awsProvider.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := awsProvider.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.SecretAccessKey != "one" || second.SecretAccessKey != "two" {
		t.Fatalf("tokens=%q,%q", first.SecretAccessKey, second.SecretAccessKey)
	}
	changed := base
	changed.Prefix = "apps/other/m/"
	p.credentials = []Credential{changed}
	p.calls = 0
	if _, err := awsProvider.Retrieve(context.Background()); err == nil {
		t.Fatal("scope change accepted")
	}
}

func TestAWSProviderFailsClosed(t *testing.T) {
	base := storageTestCredential("one")
	p := &storageProviderSequence{credentials: []Credential{base}, err: errors.New("denied")}
	if _, err := (&awsRenewableProvider{provider: p, scope: scopeFromCredential(base)}).Retrieve(context.Background()); err == nil {
		t.Fatal("provider failure silently reused token")
	}
}
