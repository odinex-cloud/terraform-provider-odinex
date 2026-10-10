package provider

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"odinex.cloud/terraform-provider-odinex/internal/client"
)

func TestMatchLabelSelector(t *testing.T) {
	labels := map[string]string{"env": "prod", "role": "web", "tier": "1"}
	cases := []struct {
		sel  string
		want bool
	}{
		{"", true},
		{"env=prod", true},
		{"env==prod", true},
		{"env=dev", false},
		{"env!=dev", true},
		{"env!=prod", false},
		{"role", true},
		{"missing", false},
		{"!missing", true},
		{"!role", false},
		{"env=prod,role=web", true},
		{"env=prod,role=db", false},
		{"env in (dev,prod)", true},
		{"env notin (dev,prod)", false},
		{"env in (dev,prod),role", true},
		{"missing notin (a)", true},
	}
	for _, c := range cases {
		got, err := matchLabelSelector(c.sel, labels)
		if err != nil {
			t.Errorf("%q: %v", c.sel, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %v, want %v", c.sel, got, c.want)
		}
	}
	if _, err := matchLabelSelector("env=prod,,role", labels); err == nil {
		t.Error("expected error for empty term")
	}
	if _, err := matchLabelSelector("env in dev", labels); err == nil {
		t.Error("expected error for set without parentheses")
	}
}

func TestDiffs(t *testing.T) {
	add, rm := diffInt64([]int64{1, 2, 3}, []int64{2, 3, 4})
	if !reflect.DeepEqual(add, []int64{4}) || !reflect.DeepEqual(rm, []int64{1}) {
		t.Errorf("diffInt64: add %v remove %v", add, rm)
	}
	sa, sr := diffStrings([]string{"a"}, []string{"a", "b"})
	if !reflect.DeepEqual(sa, []string{"b"}) || sr != nil {
		t.Errorf("diffStrings: add %v remove %v", sa, sr)
	}
	have := []client.FirewallTarget{{Type: "server", Server: ptr(int64(1))}, {Type: "label_selector", LabelSelector: ptr("a=b")}}
	want := []client.FirewallTarget{{Type: "label_selector", LabelSelector: ptr("a=b")}, {Type: "server", Server: ptr(int64(2))}}
	fa, fr := diffFirewallTargets(have, want)
	if len(fa) != 1 || *fa[0].Server != 2 || len(fr) != 1 || *fr[0].Server != 1 {
		t.Errorf("diffFirewallTargets: add %+v remove %+v", fa, fr)
	}
}

func TestLabelsConversion(t *testing.T) {
	ctx := context.Background()
	m := labelsToMap(client.Labels{"a": "1", "b": "2"})
	back, d := labelsFromMap(ctx, m)
	if d.HasError() || !reflect.DeepEqual(back, client.Labels{"a": "1", "b": "2"}) {
		t.Errorf("round trip: %v %v", back, d)
	}
	empty, _ := labelsFromMap(ctx, types.MapNull(types.StringType))
	if empty == nil || len(empty) != 0 {
		t.Errorf("null map must become empty labels, got %#v", empty)
	}
	if labelsToMap(nil).IsNull() {
		t.Error("nil labels must become an empty (non-null) map")
	}
}

func TestPickRef(t *testing.T) {
	if got := pickRef(types.StringValue("2"), "ubuntu-24.04", 2); got != "2" {
		t.Errorf("ID spelling not kept: %s", got)
	}
	if got := pickRef(types.StringValue("ubuntu-24.04"), "ubuntu-24.04", 2); got != "ubuntu-24.04" {
		t.Errorf("name spelling not kept: %s", got)
	}
	if got := pickRef(types.StringValue("debian-13"), "ubuntu-24.04", 2); got != "ubuntu-24.04" {
		t.Errorf("changed reference must return the API name: %s", got)
	}
	if got := pickRef(types.StringNull(), "", 7); got != "7" {
		t.Errorf("unnamed object must return its ID: %s", got)
	}
}

func TestIDParsers(t *testing.T) {
	sid, nid, err := parseServerNetworkID("12-34")
	if err != nil || sid != 12 || nid != 34 {
		t.Errorf("parseServerNetworkID: %d %d %v", sid, nid, err)
	}
	for _, bad := range []string{"12", "a-b", "1-2-3", "-1-2"} {
		if _, _, err := parseServerNetworkID(bad); err == nil {
			t.Errorf("parseServerNetworkID(%q) should fail", bad)
		}
	}
	tg, ip, err := parseRDNSID("s-5-2001:db8::1")
	if err != nil || tg.kind != "s" || tg.id != 5 || ip != "2001:db8::1" {
		t.Errorf("parseRDNSID: %+v %s %v", tg, ip, err)
	}
	if formatRDNSID(tg, ip) != "s-5-2001:db8::1" {
		t.Error("formatRDNSID round trip")
	}
	for _, bad := range []string{"x-1-1.2.3.4", "s-1", "s-a-1.2.3.4", "f-1-notanip"} {
		if _, _, err := parseRDNSID(bad); err == nil {
			t.Errorf("parseRDNSID(%q) should fail", bad)
		}
	}
	if !sameIP("2001:db8:0::1", "2001:db8::1") || sameIP("1.2.3.4", "1.2.3.5") {
		t.Error("sameIP")
	}
}

func TestServerToModel(t *testing.T) {
	ctx := context.Background()
	s := &client.Server{
		Id: 42, Name: "web-1", Status: "running", PrimaryDiskSize: 40,
		ServerType: client.ServerType{Id: 1, Name: "t1"},
		Location:   client.Region{Id: 1, Name: "fra1"},
		Image:      &client.Image{Id: 2, Name: ptr("ubuntu-24.04")},
		Labels:     client.Labels{"role": "web"},
		Protection: client.Protection{Delete: true},
		PrivateNet: []struct {
			Ip      *string `json:"ip"`
			Network int64   `json:"network"`
		}{{Ip: ptr("10.0.0.2"), Network: 7}},
		BackupWindow: ptr("22-02"),
	}
	s.PublicNet.Ipv4 = &client.PublicNetAddress{Id: 1, Ip: ptr("198.51.100.1")}

	// Configured with network and public_net blocks; image referenced by ID.
	m := serverModel{
		Image:     types.StringValue("2"),
		Network:   types.SetValueMust(types.ObjectType{AttrTypes: serverNetworkAttrTypes}, nil),
		PublicNet: types.ObjectValueMust(serverPublicNetAttrTypes, map[string]attr.Value{"ipv4_enabled": types.BoolValue(true), "ipv6_enabled": types.BoolValue(true)}),
		KeepDisk:  types.BoolUnknown(),
		SSHKeys:   types.ListUnknown(types.StringType),
	}
	if d := serverToModel(ctx, s, &m); d.HasError() {
		t.Fatal(d)
	}
	if m.ID.ValueString() != "42" || m.Image.ValueString() != "2" || m.ServerType.ValueString() != "t1" || m.Location.ValueString() != "fra1" {
		t.Errorf("refs: %s %s %s %s", m.ID, m.Image, m.ServerType, m.Location)
	}
	if !m.Backups.ValueBool() || m.BackupWindow.ValueString() != "22-02" || !m.DeleteProtection.ValueBool() {
		t.Errorf("flags: %v %v %v", m.Backups, m.BackupWindow, m.DeleteProtection)
	}
	if m.IPv4Address.ValueString() != "198.51.100.1" || m.IPv6Address.ValueString() != "" {
		t.Errorf("ips: %s %s", m.IPv4Address, m.IPv6Address)
	}
	v4, v6 := publicNetFlags(m.PublicNet)
	if !v4 || v6 {
		t.Errorf("public_net must reflect the API (ipv6 disabled): %v %v", v4, v6)
	}
	var nets []serverNetworkBlock
	m.Network.ElementsAs(ctx, &nets, false)
	if len(nets) != 1 || nets[0].NetworkID.ValueInt64() != 7 || nets[0].IP.ValueString() != "10.0.0.2" {
		t.Errorf("network: %+v", nets)
	}
	if m.KeepDisk.ValueBool() || !m.SSHKeys.IsNull() {
		t.Errorf("unknown write-only values must be resolved: %v %v", m.KeepDisk, m.SSHKeys)
	}

	// Without network/public_net blocks they stay null (import / server_network use).
	m2 := serverModel{Network: types.SetNull(types.ObjectType{AttrTypes: serverNetworkAttrTypes}), PublicNet: types.ObjectNull(serverPublicNetAttrTypes)}
	serverToModel(ctx, s, &m2)
	if !m2.Network.IsNull() || !m2.PublicNet.IsNull() {
		t.Error("network/public_net must stay null when not configured")
	}
	if m2.Image.ValueString() != "ubuntu-24.04" {
		t.Errorf("image after import: %s", m2.Image)
	}
}

func TestPublicNetFlags(t *testing.T) {
	v4, v6 := publicNetFlags(types.ObjectNull(serverPublicNetAttrTypes))
	if !v4 || !v6 {
		t.Error("missing block means both enabled")
	}
	o := types.ObjectValueMust(serverPublicNetAttrTypes, map[string]attr.Value{"ipv4_enabled": types.BoolValue(false), "ipv6_enabled": types.BoolNull()})
	v4, v6 = publicNetFlags(o)
	if v4 || !v6 {
		t.Errorf("got %v %v", v4, v6)
	}
}

func TestFirewallRulesRoundTrip(t *testing.T) {
	ctx := context.Background()
	api := []client.FirewallRule{
		{Direction: "in", Protocol: "tcp", Port: ptr("22"), SourceIps: &[]string{"0.0.0.0/0", "::/0"}, Description: ptr("SSH")},
		{Direction: "in", Protocol: "icmp", SourceIps: &[]string{"0.0.0.0/0"}, DestinationIps: &[]string{}, Description: ptr("")},
		{Direction: "out", Protocol: "udp", Port: ptr("53"), DestinationIps: &[]string{"1.1.1.1/32"}},
	}
	set := firewallRulesToSet(api)
	var rules []firewallRuleModel
	set.ElementsAs(ctx, &rules, false)
	for _, r := range rules {
		if r.Protocol.ValueString() == "icmp" && (!r.Port.IsNull() || !r.DestinationIPs.IsNull() || !r.Description.IsNull()) {
			t.Errorf("empty API values must be null: %+v", r)
		}
	}
	back, d := firewallRulesFromSet(ctx, set)
	if d.HasError() || len(back) != 3 {
		t.Fatalf("back: %v %v", back, d)
	}
	if !firewallRulesToSet(back).Equal(set) {
		t.Error("round trip changed the rules")
	}
}

func TestFirewallTargets(t *testing.T) {
	ctx := context.Background()
	ts := []client.FirewallTarget{{Type: "server", Server: ptr(int64(5))}, {Type: "label_selector", LabelSelector: ptr("role=web")}}
	set := firewallTargetsToSet(ts)
	back, d := firewallTargetsFromSet(ctx, set)
	if d.HasError() || len(back) != 2 {
		t.Fatalf("%v %v", back, d)
	}
	bad := types.SetValueMust(types.ObjectType{AttrTypes: firewallApplyToAttrTypes}, []attr.Value{
		types.ObjectValueMust(firewallApplyToAttrTypes, map[string]attr.Value{"server": types.Int64Value(1), "label_selector": types.StringValue("a")}),
	})
	if _, d := firewallTargetsFromSet(ctx, bad); !d.HasError() {
		t.Error("server + label_selector in one block must fail")
	}
}

func TestLBServicesKeepNull(t *testing.T) {
	ctx := context.Background()
	// Config: only protocol + listen_port + a health check with a path.
	hcHTTP := types.ObjectValueMust(lbHCHTTPAttrTypes, map[string]attr.Value{"path": types.StringValue("/health"), "status_codes": types.ListNull(types.StringType)})
	hc := types.ObjectValueMust(lbHealthCheckAttrTypes, map[string]attr.Value{
		"protocol": types.StringValue("http"), "port": types.Int64Null(), "interval": types.Int64Null(),
		"timeout": types.Int64Null(), "retries": types.Int64Null(), "http": hcHTTP,
	})
	prior := types.ListValueMust(types.ObjectType{AttrTypes: lbServiceAttrTypes}, []attr.Value{
		types.ObjectValueMust(lbServiceAttrTypes, map[string]attr.Value{
			"protocol": types.StringValue("tcp"), "listen_port": types.Int64Value(443), "destination_port": types.Int64Null(),
			"proxyprotocol": types.BoolNull(), "http": types.ObjectNull(lbServiceHTTPAttrTypes), "health_check": types.ObjectNull(lbHealthCheckAttrTypes),
		}),
		types.ObjectValueMust(lbServiceAttrTypes, map[string]attr.Value{
			"protocol": types.StringValue("http"), "listen_port": types.Int64Value(80), "destination_port": types.Int64Null(),
			"proxyprotocol": types.BoolNull(), "http": types.ObjectNull(lbServiceHTTPAttrTypes), "health_check": hc,
		}),
	})
	services, d := lbServicesFromList(ctx, prior)
	if d.HasError() || len(services) != 2 || services[1].HealthCheck == nil || *services[1].HealthCheck.Http.Path != "/health" {
		t.Fatalf("from list: %+v %v", services, d)
	}
	// The API answers sorted by port and fills in all defaults.
	api := []client.LBService{
		{Protocol: "http", ListenPort: 80, DestinationPort: ptr(80), Proxyprotocol: ptr(false)},
		{Protocol: "tcp", ListenPort: 443, DestinationPort: ptr(443), Proxyprotocol: ptr(false)},
	}
	api[0].HealthCheck = services[1].HealthCheck
	api[0].HealthCheck.Interval, api[0].HealthCheck.Timeout, api[0].HealthCheck.Retries, api[0].HealthCheck.Port = ptr(15), ptr(10), ptr(3), ptr(80)
	api[0].HealthCheck.Http.StatusCodes = &[]string{"2??"}
	got, d := lbServicesToList(ctx, api, prior)
	if d.HasError() {
		t.Fatal(d)
	}
	if !got.Equal(prior) {
		t.Errorf("API defaults leaked into state:\n got %v\nwant %v", got, prior)
	}
	// After import (no prior), everything is shown.
	imported, _ := lbServicesToList(ctx, api, types.ListNull(types.ObjectType{AttrTypes: lbServiceAttrTypes}))
	var ms []lbServiceModel
	imported.ElementsAs(ctx, &ms, false)
	if len(ms) != 2 || ms[0].DestinationPort.ValueInt64() != 80 || ms[0].HealthCheck.IsNull() {
		t.Errorf("import: %+v", ms)
	}
}

func TestLBTargets(t *testing.T) {
	ctx := context.Background()
	in := []client.LBTargetInput{{Type: "server", Server: ptr(int64(3))}, {Type: "label_selector", LabelSelector: ptr("role=web")}, {Type: "ip", Ip: ptr("203.0.113.5")}}
	set := lbTargetsToSet(in)
	back, d := lbTargetsFromSet(ctx, set)
	if d.HasError() || len(back) != 3 {
		t.Fatalf("%v %v", back, d)
	}
	keys := map[string]bool{}
	for _, b := range back {
		keys[lbTargetKey(b)] = true
	}
	for _, k := range []string{"server:3", "label_selector:role=web", "ip:203.0.113.5"} {
		if !keys[k] {
			t.Errorf("missing %s in %v", k, keys)
		}
	}
	if lbAlgorithm(types.ObjectNull(lbAlgorithmAttrTypes)) != "round_robin" {
		t.Error("default algorithm")
	}
}

func TestFilterImages(t *testing.T) {
	now := time.Now()
	imgs := []client.Image{
		{Id: 1, Name: ptr("ubuntu-22.04"), Architecture: "x86", Status: "available", Created: now.Add(-48 * time.Hour), Labels: client.Labels{}},
		{Id: 2, Name: ptr("ubuntu-24.04"), Architecture: "x86", Status: "available", Created: now.Add(-24 * time.Hour), Labels: client.Labels{}},
		{Id: 3, Type: "snapshot", Architecture: "x86", Status: "available", Created: now.Add(-2 * time.Hour), Labels: client.Labels{"app": "web"}},
		{Id: 4, Type: "snapshot", Architecture: "x86", Status: "available", Created: now.Add(-1 * time.Hour), Labels: client.Labels{"app": "web"}},
		{Id: 5, Type: "snapshot", Architecture: "x86", Status: "creating", Created: now, Labels: client.Labels{"app": "web"}},
		{Id: 6, Name: ptr("old"), Architecture: "x86", Status: "available", Created: now, Deprecated: &now, Labels: client.Labels{}},
	}
	got, err := filterImages(imgs, 0, "", "app=web", "", []string{"available"}, false)
	if err != nil || len(got) != 2 || got[0].Id != 4 {
		t.Errorf("selector + most recent: %+v %v", got, err)
	}
	got, _ = filterImages(imgs, 0, "ubuntu-24.04", "", "", nil, false)
	if len(got) != 1 || got[0].Id != 2 {
		t.Errorf("by name: %+v", got)
	}
	got, _ = filterImages(imgs, 0, "old", "", "", nil, false)
	if len(got) != 0 {
		t.Error("deprecated images are excluded by default")
	}
	got, _ = filterImages(imgs, 6, "", "", "", nil, false)
	if len(got) != 1 {
		t.Error("lookup by ID includes deprecated images")
	}
}

func TestDataSourceValues(t *testing.T) {
	st := serverTypeValues(client.ServerType{Id: 1, Name: "t1", Cores: 1, Memory: 2, Disk: 40, Prices: []client.ServerTypePrice{{Location: "fra1", PriceHourly: client.Price{Net: "0.0064", Gross: "0.0064"}}}})
	if _, err := types.ObjectValue(fieldAttrTypes(serverTypeFields), st); err != nil {
		t.Errorf("server type values do not match fields: %v", err)
	}
	loc := locationValues(client.Region{Id: 1, Name: "fra1"})
	if _, err := types.ObjectValue(fieldAttrTypes(locationFields), loc); err != nil {
		t.Errorf("location values do not match fields: %v", err)
	}
	s := client.Server{Id: 1, Name: "x"}
	if _, err := types.ObjectValue(fieldAttrTypes(serverFields), serverValues(s)); err != nil {
		t.Errorf("server values do not match fields: %v", err)
	}
	img := imageValues(client.Image{Id: 1, Name: ptr("x")})
	delete(img, "id")
	delete(img, "name")
	if _, err := types.ObjectValue(fieldAttrTypes(imageFields), img); err != nil {
		t.Errorf("image values do not match fields: %v", err)
	}
	if _, err := types.ObjectValue(fieldAttrTypes(sshKeyFields), sshKeyValues(client.SshKey{Id: 1})); err != nil {
		t.Errorf("ssh key values do not match fields: %v", err)
	}
	if _, err := types.ObjectValue(fieldAttrTypes(lbTypeFields), lbTypeValues(client.LoadBalancerType{Id: 1})); err != nil {
		t.Errorf("lb type values do not match fields: %v", err)
	}
}

func TestFirewallsOfServer(t *testing.T) {
	mk := func(id int64, targets ...client.FirewallTarget) client.Firewall {
		fw := client.Firewall{Id: id}
		for _, tg := range targets {
			fw.AppliedTo = append(fw.AppliedTo, struct {
				AppliedToServers *[]int64                     `json:"applied_to_servers,omitempty"`
				LabelSelector    *string                      `json:"label_selector,omitempty"`
				Server           *int64                       `json:"server,omitempty"`
				Type             client.FirewallAppliedToType `json:"type"`
			}{LabelSelector: tg.LabelSelector, Server: tg.Server, Type: client.FirewallAppliedToType(tg.Type)})
		}
		return fw
	}
	fws := []client.Firewall{
		mk(1, client.FirewallTarget{Type: "server", Server: ptr(int64(7))}),
		mk(2, client.FirewallTarget{Type: "label_selector", LabelSelector: ptr("role=web")}),
		mk(3, client.FirewallTarget{Type: "server", Server: ptr(int64(8))}, client.FirewallTarget{Type: "server", Server: ptr(int64(7))}),
		mk(4),
	}
	if got := firewallsOfServer(fws, 7); !reflect.DeepEqual(got, []int64{1, 3}) {
		t.Errorf("got %v; label-selector firewalls must not be listed", got)
	}
	if got := firewallsOfServer(fws, 9); got == nil || len(got) != 0 {
		t.Errorf("got %#v, want empty non-nil", got)
	}
}

func TestVPCToModel(t *testing.T) {
	var nm networkModel
	networkToModel(&client.Network{Id: 3, Name: "n", IpRange: "10.0.0.0/16", Gateway: "10.0.0.1", NatGateway: ptr(int64(9))}, &nm)
	if nm.Gateway.ValueString() != "10.0.0.1" || nm.NatGatewayID.ValueInt64() != 9 {
		t.Errorf("network: %s %s", nm.Gateway, nm.NatGatewayID)
	}
	networkToModel(&client.Network{Id: 3, Gateway: "10.0.0.1"}, &nm)
	if !nm.NatGatewayID.IsNull() {
		t.Error("nat_gateway_id must be null without a NAT gateway")
	}

	var gm natGatewayModel
	natGatewayToModel(&client.NatGateway{Id: 9, Name: "gw", Network: 3, Location: "fra1", PrivateIp: "10.0.0.1", Status: "creating", Labels: client.Labels{}}, &gm)
	if gm.ID.ValueString() != "9" || gm.NetworkID.ValueInt64() != 3 || gm.PrivateIP.ValueString() != "10.0.0.1" || gm.PublicIP.ValueString() != "" {
		t.Errorf("nat gateway: %+v", gm)
	}

	var em elasticIPModel
	elasticIPToModel(&client.ElasticIp{Id: 5, Name: "eip", Location: "fra1", Ip: ptr("203.0.113.7"), Server: ptr(int64(42)), Protection: client.Protection{Delete: true}}, &em)
	if em.IPAddress.ValueString() != "203.0.113.7" || em.ServerID.ValueInt64() != 42 || em.Location.ValueString() != "fra1" || !em.DeleteProtection.ValueBool() {
		t.Errorf("elastic ip: %+v", em)
	}
	elasticIPToModel(&client.ElasticIp{Id: 5}, &em)
	if !em.ServerID.IsNull() {
		t.Error("server_id must be null when unassigned")
	}

	// A server without public IPs.
	s := &client.Server{Id: 42, PrimaryNetwork: ptr(int64(3))}
	s.PublicNet.ElasticIp = &struct {
		Id int64   `json:"id"`
		Ip *string `json:"ip"`
	}{Id: 5, Ip: ptr("203.0.113.7")}
	sm := serverModel{Network: types.SetNull(types.ObjectType{AttrTypes: serverNetworkAttrTypes}), PublicNet: types.ObjectNull(serverPublicNetAttrTypes)}
	serverToModel(context.Background(), s, &sm)
	if sm.PrimaryNetworkID.ValueInt64() != 3 || sm.IPv4Address.ValueString() != "" || !sm.PrimaryIPv4ID.IsNull() {
		t.Errorf("server: %s %s %s", sm.PrimaryNetworkID, sm.IPv4Address, sm.PrimaryIPv4ID)
	}
	v := serverValues(*s)
	if v["primary_network_id"].(types.Int64).ValueInt64() != 3 || v["elastic_ip_address"].(types.String).ValueString() != "203.0.113.7" {
		t.Errorf("server data source: %v", v)
	}
	if _, err := types.ObjectValue(fieldAttrTypes(serverFields), v); err != nil {
		t.Errorf("server values do not match fields: %v", err)
	}
}

func TestPrimaryNetwork(t *testing.T) {
	ctx := context.Background()
	netType := types.ObjectType{AttrTypes: serverNetworkAttrTypes}
	block := func(id int64, ip string) attr.Value {
		v := types.StringNull()
		if ip != "" {
			v = types.StringValue(ip)
		}
		return types.ObjectValueMust(serverNetworkAttrTypes, map[string]attr.Value{"network_id": types.Int64Value(id), "ip": v})
	}
	private := types.ObjectValueMust(serverPublicNetAttrTypes, map[string]attr.Value{"ipv4_enabled": types.BoolValue(false), "ipv6_enabled": types.BoolValue(false)})
	public := types.ObjectNull(serverPublicNetAttrTypes)
	cases := []struct {
		name    string
		pub     types.Object
		primary types.Int64
		nets    []attr.Value
		wantErr bool
		want    *int64
	}{
		{"public, no primary", public, types.Int64Null(), []attr.Value{block(1, ""), block(2, "")}, false, nil},
		{"public with primary", public, types.Int64Value(1), []attr.Value{block(1, "")}, true, ptr(int64(1))},
		{"private, one network", private, types.Int64Null(), []attr.Value{block(1, "")}, false, ptr(int64(1))},
		{"private, no network", private, types.Int64Null(), nil, true, nil},
		{"private, two networks without primary", private, types.Int64Null(), []attr.Value{block(1, ""), block(2, "")}, true, nil},
		{"private, primary among two", private, types.Int64Value(2), []attr.Value{block(1, "10.0.0.5"), block(2, "")}, false, ptr(int64(2))},
		{"private, primary not listed", private, types.Int64Value(3), []attr.Value{block(1, "")}, true, ptr(int64(3))},
		{"private, primary with fixed IP", private, types.Int64Value(1), []attr.Value{block(1, "10.0.0.5")}, true, ptr(int64(1))},
		{"private, single network with fixed IP", private, types.Int64Null(), []attr.Value{block(1, "10.0.0.5")}, true, ptr(int64(1))},
	}
	for _, c := range cases {
		m := serverModel{PublicNet: c.pub, PrimaryNetworkID: c.primary, Network: types.SetValueMust(netType, c.nets)}
		if c.nets == nil {
			m.Network = types.SetNull(netType)
		}
		if d := validatePrimaryNetwork(ctx, m); d.HasError() != c.wantErr {
			t.Errorf("%s: validation errors %v, want error %v", c.name, d, c.wantErr)
		}
		var nets []serverNetworkBlock
		if c.nets != nil {
			m.Network.ElementsAs(ctx, &nets, false)
		}
		got := primaryNetwork(m, nets)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: primaryNetwork = %v, want %v", c.name, got, c.want)
		}
	}
	// primary_network_id without network blocks (odinex_server_network manages the others).
	m := serverModel{PublicNet: private, PrimaryNetworkID: types.Int64Value(4), Network: types.SetNull(netType)}
	if d := validatePrimaryNetwork(ctx, m); d.HasError() {
		t.Errorf("primary without network blocks: %v", d)
	}
}

func TestLBNetwork(t *testing.T) {
	ctx := context.Background()
	r := &loadBalancerResource{}
	lb := &client.LoadBalancer{Id: 1, Algorithm: "round_robin", Network: ptr(int64(3))}
	m := loadBalancerModel{
		Algorithm: types.ObjectNull(lbAlgorithmAttrTypes),
		Service:   types.ListNull(types.ObjectType{AttrTypes: lbServiceAttrTypes}),
		Target:    types.SetNull(types.ObjectType{AttrTypes: lbTargetAttrTypes}),
	}
	if d := r.toModel(ctx, lb, &m); d.HasError() {
		t.Fatal(d)
	}
	if m.NetworkID.ValueInt64() != 3 {
		t.Errorf("network_id: %s", m.NetworkID)
	}
	lb.Network = nil
	r.toModel(ctx, lb, &m)
	if !m.NetworkID.IsNull() {
		t.Error("network_id must be null without a network")
	}
}
