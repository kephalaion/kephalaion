package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/kephalaion/kephalaion/internal/config"
	"github.com/kephalaion/kephalaion/internal/contract"
	hubstore "github.com/kephalaion/kephalaion/internal/hub/store"
	nodestore "github.com/kephalaion/kephalaion/internal/node/store"
)

// exportFormat ist die Fassung des Exportformats, die dieses Binary schreibt.
// Gelesen werden auch ältere Fassungen ab minExportFormat. Format 3 bringt
// hubs.node_name am Node; ein Hub-Eintrag ohne ihn scheitert beim Import an
// derselben Prüfung wie node hub add ohne --node. Format 4 bringt die Accounts
// des Hubs samt Rechten; ein Export vor Format 4 lässt die Accounts beim
// Import, wie sie sind. Format 5 bringt den User je Account, dort Pflicht; ein
// Import von Format 4 setzt ihn auf den Namen des Accounts. Format 6 bringt
// je Recht die Scopes vendor/<name> (vendor, eine Liste); ein Export davor
// darf sie nicht tragen und liest sich ohne Scopes. Format 7 bringt je
// Hub-Eintrag des Nodes die CA (ca, PEM-Text, bei https); ein Export davor
// darf sie nicht tragen und liest sich ohne CA.
const (
	exportFormat    = 7
	minExportFormat = 1
	// accountsFormat ist die erste Fassung mit Accounts.
	accountsFormat = 4
	// userFormat ist die erste Fassung mit dem User je Account.
	userFormat = 5
	// vendorFormat ist die erste Fassung mit den Scopes vendor/<name>.
	vendorFormat = 6
	// caFormat ist die erste Fassung mit der CA je Hub-Eintrag.
	caFormat = 7
)

// exportFile ist der Inhalt einer Exportdatei: die config, die settings je
// Rolle und ab Format 2 die lokalen Tabellen je Rolle — keine Inhalte, kein
// db_info, kein Protokoll.
type exportFile struct {
	Format   int                               `yaml:"format"`
	Config   config.Config                     `yaml:"config"`
	Settings map[config.Role]map[string]string `yaml:"settings"`
	Tables   *exportTables                     `yaml:"tables,omitempty"`
}

// exportTables sind die lokalen Tabellen je Rolle; eine Rolle fehlt nur, wenn
// sie nicht eingerichtet ist.
type exportTables struct {
	Hub  *hubTablesYAML  `yaml:"hub,omitempty"`
	Node *nodeTablesYAML `yaml:"node,omitempty"`
}

type hubTablesYAML struct {
	Collections     []collectionYAML `yaml:"collections"`
	Nodes           []nodeYAML       `yaml:"nodes"`
	NodeCollections []grantYAML      `yaml:"node_collections"`
	// Accounts fehlt vor Format 4.
	Accounts []accountYAML `yaml:"accounts"`
}

// accountYAML ist ein Account im Export: die Zeile aus accounts und die
// Rechte je Collection — bei einem gesperrten Account die gemerkten. User
// ist ab Format 5 Pflicht und darf davor nicht dastehen; das prüft
// parseExport am YAML-Knoten.
type accountYAML struct {
	Name        string      `yaml:"name"`
	User        string      `yaml:"user"`
	Description string      `yaml:"description"`
	TokenHash   string      `yaml:"token_hash"`
	Locked      bool        `yaml:"locked"`
	CreatedAt   int64       `yaml:"created_at"`
	CreatedBy   string      `yaml:"created_by"`
	Rights      []rightYAML `yaml:"rights"`
}

// rightYAML sind die Rechte eines Accounts in einer Collection. Vendor sind
// die Scopes vendor/<name>, ab Format 6, immer als Liste (auch leer); davor
// darf das Feld nicht dastehen — das prüft parseExport am YAML-Knoten.
type rightYAML struct {
	Collection string   `yaml:"collection"`
	Write      bool     `yaml:"write"`
	Supersede  bool     `yaml:"supersede"`
	Vendor     []string `yaml:"vendor"`
}

type collectionYAML struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	CreatedAt   int64  `yaml:"created_at"`
	CreatedBy   string `yaml:"created_by"`
}

type nodeYAML struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	TokenHash   string `yaml:"token_hash"`
	Locked      bool   `yaml:"locked"`
	CreatedAt   int64  `yaml:"created_at"`
	CreatedBy   string `yaml:"created_by"`
}

type grantYAML struct {
	Node       string `yaml:"node"`
	Collection string `yaml:"collection"`
}

type nodeTablesYAML struct {
	Hubs           []hubYAML    `yaml:"hubs"`
	HubCollections []wantedYAML `yaml:"hub_collections"`
}

// hubYAML ist ein Hub-Eintrag des Nodes im Export. CA ist ab Format 7 die
// CA als PEM-Text (mehrzeilig, öffentlich), leer ohne; davor darf das Feld
// nicht dastehen — das prüft parseExport am YAML-Knoten.
type hubYAML struct {
	Name      string `yaml:"name"`
	NodeName  string `yaml:"node_name"`
	Transport string `yaml:"transport"`
	Address   string `yaml:"address"`
	Token     string `yaml:"token"`
	SSHKey    string `yaml:"ssh_key"`
	CA        string `yaml:"ca"`
	HubID     string `yaml:"hub_id"`
}

type wantedYAML struct {
	Hub        string `yaml:"hub"`
	Collection string `yaml:"collection"`
}

func hubTablesToYAML(t hubstore.Tables) *hubTablesYAML {
	out := &hubTablesYAML{
		Collections:     []collectionYAML{},
		Nodes:           []nodeYAML{},
		NodeCollections: []grantYAML{},
	}
	for _, c := range t.Collections {
		out.Collections = append(out.Collections, collectionYAML(c))
	}
	for _, n := range t.Nodes {
		out.Nodes = append(out.Nodes, nodeYAML{Name: n.Name, Description: n.Description, TokenHash: n.TokenHash,
			Locked: n.Locked, CreatedAt: n.CreatedAt, CreatedBy: n.CreatedBy})
	}
	for _, g := range t.Grants {
		out.NodeCollections = append(out.NodeCollections, grantYAML(g))
	}
	out.Accounts = []accountYAML{}
	for _, a := range t.Accounts {
		y := accountYAML{Name: a.Name, User: a.User, Description: a.Description, TokenHash: a.TokenHash, Locked: a.Locked,
			CreatedAt: a.CreatedAt, CreatedBy: a.CreatedBy, Rights: []rightYAML{}}
		for _, r := range a.Rights {
			y.Rights = append(y.Rights, rightYAML{Collection: r.Collection, Write: r.Write, Supersede: r.Supersede,
				Vendor: append([]string{}, r.Vendor...)})
		}
		out.Accounts = append(out.Accounts, y)
	}
	return out
}

// toStore liefert die Tabellen für den Import. Vor Format 4 gibt es keine
// Accounts im Export; der Import lässt sie dann, wie sie sind. Vor Format 5
// ist der User der Name des Accounts; ab Format 5 hat parseExport geprüft,
// dass er dasteht (leer oder ungültig prüft hubstore.CheckTables). Vor
// Format 6 gibt es keine Scopes; ab Format 6 sind sie eine Liste, fehlend
// leer (ihre Namen prüft hubstore.CheckTables).
func (y *hubTablesYAML) toStore(format int) hubstore.Tables {
	t := hubstore.Tables{Collections: []hubstore.Collection{}, Nodes: []hubstore.Node{}, Grants: []hubstore.Grant{},
		Accounts: []hubstore.Account{}, KeepAccounts: format < accountsFormat}
	for _, a := range y.Accounts {
		user := a.Name
		if format >= userFormat {
			user = a.User
		}
		acc := hubstore.Account{Name: a.Name, User: user, Description: a.Description, TokenHash: a.TokenHash, Locked: a.Locked,
			CreatedAt: a.CreatedAt, CreatedBy: a.CreatedBy, Rights: []hubstore.AccountRight{}}
		for _, r := range a.Rights {
			acc.Rights = append(acc.Rights, hubstore.AccountRight{Collection: r.Collection,
				Rights: contract.Rights{Write: r.Write, Supersede: r.Supersede, Vendor: r.Vendor}})
		}
		t.Accounts = append(t.Accounts, acc)
	}
	for _, c := range y.Collections {
		t.Collections = append(t.Collections, hubstore.Collection(c))
	}
	for _, n := range y.Nodes {
		t.Nodes = append(t.Nodes, hubstore.Node{Name: n.Name, Description: n.Description, TokenHash: n.TokenHash,
			Locked: n.Locked, CreatedAt: n.CreatedAt, CreatedBy: n.CreatedBy})
	}
	for _, g := range y.NodeCollections {
		t.Grants = append(t.Grants, hubstore.Grant(g))
	}
	return t
}

func nodeTablesToYAML(t nodestore.Tables) *nodeTablesYAML {
	out := &nodeTablesYAML{Hubs: []hubYAML{}, HubCollections: []wantedYAML{}}
	for _, h := range t.Hubs {
		out.Hubs = append(out.Hubs, hubYAML{Name: h.Name, NodeName: h.NodeName, Transport: h.Transport, Address: h.Address,
			Token: h.Token, SSHKey: h.SSHKey, CA: h.CA, HubID: h.HubID})
	}
	for _, w := range t.Wanted {
		out.HubCollections = append(out.HubCollections, wantedYAML(w))
	}
	return out
}

// toStore liefert die Tabellen des Nodes für den Import. Vor Format 7 gibt
// es keine CA; ab Format 7 hat parseExport geprüft, dass sie nur dort steht
// (Inhalt und Transport prüft nodestore.CheckTables).
func (y *nodeTablesYAML) toStore() nodestore.Tables {
	t := nodestore.Tables{Hubs: []nodestore.Hub{}, Wanted: []nodestore.Wanted{}}
	for _, h := range y.Hubs {
		t.Hubs = append(t.Hubs, nodestore.Hub{Name: h.Name, NodeName: h.NodeName, Transport: h.Transport, Address: h.Address,
			Token: h.Token, SSHKey: h.SSHKey, CA: h.CA, HubID: h.HubID})
	}
	for _, w := range y.HubCollections {
		t.Wanted = append(t.Wanted, nodestore.Wanted(w))
	}
	return t
}

// roles liefert die Rollen eines Exports: die aus der config und die mit
// einem Teil.
func (e *exportFile) roles() ([]config.Role, error) {
	for r := range e.Settings {
		if r != config.Hub && r != config.Node {
			return nil, fmt.Errorf("unbekannte Rolle %q", r)
		}
	}
	var out []config.Role
	for _, r := range config.Roles {
		_, hasSettings := e.Settings[r]
		hasTables := e.Tables != nil && ((r == config.Hub && e.Tables.Hub != nil) || (r == config.Node && e.Tables.Node != nil))
		if e.Config.Section(r) != nil || hasSettings || hasTables {
			out = append(out, r)
		}
	}
	return out, nil
}

// tableKeys sind die Tabellen je Rolle, wie sie im Export heißen; ab
// accountsFormat kommt am Hub accounts dazu.
var tableKeys = map[config.Role][]string{
	config.Hub:  {"collections", "nodes", "node_collections"},
	config.Node: {"hubs", "hub_collections"},
}

// tableKeysOf liefert die Tabellen einer Rolle in einer Fassung.
func tableKeysOf(r config.Role, format int) []string {
	keys := tableKeys[r]
	if r == config.Hub && format >= accountsFormat {
		keys = append(append([]string{}, keys...), "accounts")
	}
	return keys
}

// parseExport liest eine Exportdatei. Die Fassung wird zuerst geprüft, damit
// ein Export einer anderen Fassung klar abgelehnt wird, statt an unbekannten
// Feldern zu scheitern. Danach muss jede Rolle des Exports jeden Teil ihrer
// Fassung haben: Ein fehlender Teil oder null bricht ab — nur ein
// ausdrücklich leerer Teil ({} bzw. []) leert beim Import.
func parseExport(data []byte) (exportFile, error) {
	var head struct {
		Format int `yaml:"format"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return exportFile{}, fmt.Errorf("kein gültiges YAML: %w", err)
	}
	if head.Format < minExportFormat || head.Format > exportFormat {
		if head.Format == 0 {
			return exportFile{}, errors.New("keine Fassung des Formats angegeben (format:) — kein Export von kephalaion?")
		}
		return exportFile{}, fmt.Errorf("unbekannte Fassung des Formats %d; dieses Binary kennt %d bis %d",
			head.Format, minExportFormat, exportFormat)
	}
	var exp exportFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&exp); err != nil {
		return exportFile{}, fmt.Errorf("Export nicht lesbar: %w", err)
	}
	if exp.Format < 2 && exp.Tables != nil {
		return exportFile{}, fmt.Errorf("Format %d kennt keine Tabellen (tables:)", exp.Format)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return exportFile{}, fmt.Errorf("kein gültiges YAML: %w", err)
	}
	roles, err := exp.roles()
	if err != nil {
		return exportFile{}, err
	}
	for _, r := range roles {
		if err := requirePart(&root, "settings", string(r)); err != nil {
			return exportFile{}, err
		}
		if exp.Format < 2 {
			continue
		}
		if err := requirePart(&root, "tables", string(r)); err != nil {
			return exportFile{}, err
		}
		// Vor Format 4 lehnt jeder Accounts-Teil ab, auch null und [] —
		// geprüft am YAML-Knoten, denn null decodiert wie ein fehlender Teil.
		if r == config.Hub && exp.Format < accountsFormat && hasPart(&root, "tables", "hub", "accounts") {
			return exportFile{}, fmt.Errorf("Format %d kennt keine Accounts (tables.hub.accounts)", exp.Format)
		}
		for _, k := range tableKeysOf(r, exp.Format) {
			if err := requirePart(&root, "tables", string(r), k); err != nil {
				return exportFile{}, err
			}
		}
	}
	if err := checkAccountUsers(&root, exp.Format); err != nil {
		return exportFile{}, err
	}
	if err := checkAccountVendor(&root, exp.Format); err != nil {
		return exportFile{}, err
	}
	if err := checkHubCA(&root, exp.Format); err != nil {
		return exportFile{}, err
	}
	return exp, nil
}

// checkHubCA prüft die CA je Hub-Eintrag gegen die Fassung, am YAML-Knoten:
// Vor Format 7 darf ca nicht dastehen, in keiner Form (auch nicht null oder
// leer). Ab Format 7 ist es Text, fehlend oder null leer; Inhalt und
// Transport prüft nodestore.CheckTables.
func checkHubCA(root *yaml.Node, format int) error {
	if format >= caFormat {
		return nil
	}
	hubs := partNode(root, "tables", "node", "hubs")
	if hubs == nil || hubs.Kind != yaml.SequenceNode {
		return nil
	}
	for i, item := range hubs.Content {
		if mappingValue(item, "ca") != nil {
			return fmt.Errorf("Format %d kennt keine CA (tables.node.hubs[%d].ca); ab Format %d", format, i, caFormat)
		}
	}
	return nil
}

// checkAccountVendor prüft die Scopes je Recht gegen die Fassung, am
// YAML-Knoten: Vor Format 6 darf vendor nicht dastehen, in keiner Form (auch
// nicht null oder leer). Ab Format 6 ist es eine Liste, fehlend oder null
// leer; die Namen prüft hubstore.CheckTables.
func checkAccountVendor(root *yaml.Node, format int) error {
	if format >= vendorFormat {
		return nil
	}
	accounts := partNode(root, "tables", "hub", "accounts")
	if accounts == nil || accounts.Kind != yaml.SequenceNode {
		return nil
	}
	for i, item := range accounts.Content {
		rights := mappingValue(item, "rights")
		if rights == nil || rights.Kind != yaml.SequenceNode {
			continue
		}
		for j, r := range rights.Content {
			if mappingValue(r, "vendor") != nil {
				return fmt.Errorf("Format %d kennt keinen Scope vendor (tables.hub.accounts[%d].rights[%d].vendor); ab Format %d",
					format, i, j, vendorFormat)
			}
		}
	}
	return nil
}

// checkAccountUsers prüft den User je Account gegen die Fassung, am
// YAML-Knoten, denn null decodiert wie ein fehlender Wert: Ab Format 5 muss
// user dastehen und darf nicht null sein; davor darf er nicht dastehen, in
// keiner Form. Leer, ungültig oder admin prüft hubstore.CheckTables —
// dieselbe Prüfung wie die CLI.
func checkAccountUsers(root *yaml.Node, format int) error {
	accounts := partNode(root, "tables", "hub", "accounts")
	if accounts == nil || accounts.Kind != yaml.SequenceNode {
		return nil
	}
	for i, item := range accounts.Content {
		if item.Kind == yaml.AliasNode {
			item = item.Alias
		}
		user := mappingValue(item, "user")
		switch {
		case format >= userFormat && user == nil:
			return fmt.Errorf("tables.hub.accounts[%d]: user fehlt; ab Format %d ist er Pflicht", i, userFormat)
		case format >= userFormat && user.Kind == yaml.ScalarNode && user.ShortTag() == "!!null":
			return fmt.Errorf("tables.hub.accounts[%d]: user ist null; ab Format %d ist er Pflicht", i, userFormat)
		case format < userFormat && user != nil:
			return fmt.Errorf("Format %d kennt keinen User (tables.hub.accounts[%d].user)", format, i)
		}
	}
	return nil
}

// partNode liefert den Knoten unter path, oder nil, wenn er fehlt.
func partNode(root *yaml.Node, path ...string) *yaml.Node {
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	for _, key := range path {
		if n = mappingValue(n, key); n == nil {
			return nil
		}
	}
	return n
}

// mappingValue liefert den Wert zu key in einer Mapping, Aliase aufgelöst,
// oder nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for j := 0; j+1 < len(n.Content); j += 2 {
		if n.Content[j].Value == key {
			v := n.Content[j+1]
			if v.Kind == yaml.AliasNode {
				v = v.Alias
			}
			return v
		}
	}
	return nil
}

// hasPart sagt, ob der Schlüssel unter path im Export steht, mit welchem
// Wert auch immer (null eingeschlossen).
func hasPart(root *yaml.Node, path ...string) bool {
	return partNode(root, path...) != nil
}

// requirePart prüft, dass der Teil unter path im Export steht und nicht null
// ist.
func requirePart(root *yaml.Node, path ...string) error {
	name := strings.Join(path, ".")
	const hint = "; ein leerer Teil muss ausdrücklich dastehen ({} bzw. [])"
	n := root
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		n = n.Content[0]
	}
	for i, key := range path {
		if n.Kind != yaml.MappingNode {
			return errors.New(name + " fehlt" + hint)
		}
		var next *yaml.Node
		for j := 0; j+1 < len(n.Content); j += 2 {
			if n.Content[j].Value == key {
				next = n.Content[j+1]
				break
			}
		}
		if next == nil {
			return errors.New(name + " fehlt" + hint)
		}
		if next.Kind == yaml.AliasNode {
			next = next.Alias
		}
		if next.Kind == yaml.ScalarNode && next.ShortTag() == "!!null" {
			if i < len(path)-1 {
				return errors.New(name + " fehlt" + hint)
			}
			return errors.New(name + " ist null" + hint)
		}
		n = next
	}
	return nil
}
