package firewall

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls     []string
	responses map[string]string
	fail      map[string]error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (string, error) {
	cl := strings.Join(args, " ")
	f.calls = append(f.calls, cl)
	if err := f.fail[cl]; err != nil {
		return "", err
	}
	return f.responses[cl], nil
}

func probe(active map[string]bool, ufwConf string) Probe {
	return Probe{
		ServiceActive: func(n string) bool { return active[n] },
		HasBinary:     func(string) bool { return true },
		ReadFile: func(string) ([]byte, error) {
			if ufwConf == "" {
				return nil, errors.New("missing")
			}
			return []byte(ufwConf), nil
		},
	}
}

func TestDetect(t *testing.T) {
	if b, err := Detect(probe(map[string]bool{"ufw": true}, "ENABLED=yes\n")); err != nil || b.Name() != "ufw" {
		t.Fatalf("ufw: %v %v", b, err)
	}
	if _, err := Detect(probe(map[string]bool{"ufw": true}, "# comment\nENABLED=no\n")); !errors.Is(err, ErrNoFirewall) {
		t.Fatalf("disabled ufw should count as no firewall, got %v", err)
	}
	if b, err := Detect(probe(map[string]bool{"firewalld": true, "ufw": true}, "ENABLED=yes")); err != nil || b.Name() != "firewalld" {
		t.Fatalf("firewalld: %v %v", b, err)
	}
	var unsupported *UnsupportedError
	if _, err := Detect(probe(map[string]bool{"nftables": true}, "")); !errors.As(err, &unsupported) {
		t.Fatalf("nftables: %v", err)
	}
	if _, err := Detect(probe(nil, "")); !errors.Is(err, ErrNoFirewall) {
		t.Fatalf("none: %v", err)
	}
}

var home = Rule{Port: 47823, MDNS: true, Networks: []Network{{Subnet: "192.168.0.0/24", Interface: "wlp2s0"}}}

func TestUFWAllow(t *testing.T) {
	f := &fakeRunner{}
	if err := (UFW{}).Allow(context.Background(), f, home); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ufw allow from 192.168.0.0/24 to any port 47823 proto tcp comment LuniBoard",
		"ufw allow from 192.168.0.0/24 to any port 5353 proto udp comment LuniBoard mDNS",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
}

func TestUFWRemoveTaggedRulesOnly(t *testing.T) {
	status := `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere
[ 2] 47823/tcp                  ALLOW IN    192.168.0.0/24             # Universal Clipboard
[ 3] 47823/tcp                  ALLOW IN    192.168.0.0/24             # LuniBoard
[ 4] 5353/udp                   ALLOW IN    192.168.0.0/24             # LuniBoard mDNS
[10] 47823/tcp                  ALLOW IN    10.0.0.0/8                 # LuniBoard
`
	f := &fakeRunner{responses: map[string]string{"ufw status numbered": status}}
	if err := (UFW{}).Remove(context.Background(), f, home); err != nil {
		t.Fatal(err)
	}
	want := "ufw status numbered\nufw --force delete 10\nufw --force delete 4\nufw --force delete 3"
	if got := strings.Join(f.calls, "\n"); got != want {
		t.Fatalf("calls:\n%s", got)
	}
}

func TestFirewalldAllowUsesInterfaceZone(t *testing.T) {
	f := &fakeRunner{responses: map[string]string{"firewall-cmd --get-zone-of-interface=wlp2s0": "home\n"}}
	if err := (Firewalld{}).Allow(context.Background(), f, home); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls, "\n")
	for _, want := range []string{
		`firewall-cmd --permanent --zone=home --add-rich-rule=rule family="ipv4" source address="192.168.0.0/24" port port="47823" protocol="tcp" accept`,
		`firewall-cmd --permanent --zone=home --add-rich-rule=rule family="ipv4" source address="192.168.0.0/24" service name="mdns" accept`,
		"firewall-cmd --reload",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
}

func TestFirewalldFallsBackToDefaultZone(t *testing.T) {
	f := &fakeRunner{
		responses: map[string]string{"firewall-cmd --get-default-zone": "public\n"},
		fail:      map[string]error{"firewall-cmd --get-zone-of-interface=wlp2s0": errors.New("no zone")},
	}
	if err := (Firewalld{}).Allow(context.Background(), f, home); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "--zone=public") {
		t.Fatalf("default zone not used:\n%s", strings.Join(f.calls, "\n"))
	}
}

func TestFirewalldRemoveOnlyMatchingRules(t *testing.T) {
	f := &fakeRunner{responses: map[string]string{
		"firewall-cmd --get-zones":                               "home public\n",
		"firewall-cmd --permanent --zone=home --list-rich-rules": "rule family=\"ipv4\" source address=\"192.168.0.0/24\" port port=\"47823\" protocol=\"tcp\" accept\nrule family=\"ipv4\" source address=\"192.168.0.0/24\" port port=\"22\" protocol=\"tcp\" accept\n",
	}}
	if err := (Firewalld{}).Remove(context.Background(), f, home); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls, "\n")
	if !strings.Contains(joined, `--remove-rich-rule=rule family="ipv4" source address="192.168.0.0/24" port port="47823"`) {
		t.Fatalf("LuniBoard rule not removed:\n%s", joined)
	}
	if strings.Contains(joined, `port="22"" protocol`) || strings.Contains(joined, `--remove-rich-rule=rule family="ipv4" source address="192.168.0.0/24" port port="22"`) {
		t.Fatal("removed an unrelated rule")
	}
}

func TestRuleValidate(t *testing.T) {
	for _, r := range []Rule{
		{Port: 0, Networks: home.Networks},
		{Port: 47823},
		{Port: 47823, Networks: []Network{{Subnet: "192.168.0.0/24; rm -rf /"}}},
	} {
		if r.Validate() == nil {
			t.Errorf("expected invalid: %+v", r)
		}
	}
	if err := (UFW{}).Allow(context.Background(), &fakeRunner{}, Rule{Port: 47823}); err == nil {
		t.Fatal("Allow must validate")
	}
}
