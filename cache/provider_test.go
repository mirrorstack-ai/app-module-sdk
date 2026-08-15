package cache

import (
	"context"
	"errors"
	"testing"
)

type cacheProviderSequence struct {
	credentials []Credential
	calls       int
	err         error
}

func (p *cacheProviderSequence) Credential(context.Context) (Credential, error) {
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

func TestRedisCredentialsProviderRotatesAndPinsScope(t *testing.T) {
	base := Credential{Endpoint: "cache.example:6379", Username: "role", Token: "one"}
	rotated := base
	rotated.Token = "two"
	p := &cacheProviderSequence{credentials: []Credential{base, base, rotated}}
	opts, err := optionsFromProvider(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	user, token, err := opts.CredentialsProviderContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user != "role" || token != "one" {
		t.Fatalf("first credentials=%q/%q", user, token)
	}
	_, token, err = opts.CredentialsProviderContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "two" {
		t.Fatalf("rotated token=%q", token)
	}
	changed := base
	changed.Endpoint = "other:6379"
	p.credentials = []Credential{changed}
	p.calls = 0
	if _, _, err := opts.CredentialsProviderContext(context.Background()); err == nil {
		t.Fatal("scope change accepted")
	}
}

func TestRedisCredentialsProviderFailsClosed(t *testing.T) {
	base := Credential{Endpoint: "cache.example:6379", Username: "role", Token: "one"}
	p := &cacheProviderSequence{credentials: []Credential{base, base}}
	opts, err := optionsFromProvider(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.err = errors.New("denied")
	if _, _, err := opts.CredentialsProviderContext(context.Background()); err == nil {
		t.Fatal("provider failure silently reused token")
	}
}
