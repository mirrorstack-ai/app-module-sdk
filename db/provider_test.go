package db

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type rotatingDBProvider struct {
	mu          sync.Mutex
	credentials []Credential
	calls       int
	err         error
}

func (p *rotatingDBProvider) Credential(context.Context) (Credential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
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

func TestRenewablePoolBeforeConnectGetsRotatedToken(t *testing.T) {
	base := Credential{Host: "db.example", Port: 5432, Database: "app", Username: "role", Token: "token-one"}
	rotated := base
	rotated.Token = "token-two"
	p := &rotatingDBProvider{credentials: []Credential{base, rotated}}
	cfg, err := createPoolConfig(base, p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConnLifetime != renewableMaxConnLifetime {
		t.Fatalf("MaxConnLifetime=%v", cfg.MaxConnLifetime)
	}
	if cfg.ConnConfig.Password != "" {
		t.Fatal("renewable pool retained the initial token in its base config")
	}
	first := cfg.ConnConfig.Copy()
	if err := cfg.BeforeConnect(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := cfg.ConnConfig.Copy()
	if err := cfg.BeforeConnect(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if first.Password != "token-one" || second.Password != "token-two" || p.calls != 2 {
		t.Fatalf("passwords=%q,%q calls=%d", first.Password, second.Password, p.calls)
	}
}

func TestRenewablePoolRejectsScopeChangeAndProviderFailure(t *testing.T) {
	base := Credential{Host: "db.example", Port: 5432, Database: "app", Username: "role", Token: "one"}
	changed := base
	changed.Database = "other"
	p := &rotatingDBProvider{credentials: []Credential{changed}}
	cfg, _ := createPoolConfig(base, p)
	if err := cfg.BeforeConnect(context.Background(), cfg.ConnConfig.Copy()); err == nil {
		t.Fatal("scope change accepted")
	}
	p = &rotatingDBProvider{credentials: []Credential{base}, err: errors.New("refresh denied")}
	cfg, _ = createPoolConfig(base, p)
	if err := cfg.BeforeConnect(context.Background(), cfg.ConnConfig.Copy()); err == nil {
		t.Fatal("provider failure silently reused token")
	}
}
