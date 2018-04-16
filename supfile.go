package sup

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/pkg/errors"

	"gopkg.in/yaml.v2"
)

// Supfile represents the Stack Up configuration YAML file.
type Supfile struct {
	Networks Networks  `yaml:"networks"`
	Commands Commands  `yaml:"commands"`
	Targets  Targets   `yaml:"targets"`
	Env      EnvList   `yaml:"env"`
	Version  string    `yaml:"version"`
	Includes []Include `yaml:"includes"`
}

// Network is group of hosts with extra custom env vars.
type Network struct {
	Env       EnvList  `yaml:"env"`
	Inventory string   `yaml:"inventory"`
	Hosts     []string `yaml:"hosts"`
	Bastion   string   `yaml:"bastion"` // Jump host for the environment

	// Should these live on Hosts too? We'd have to change []string to struct, even in Supfile.
	User         string // `yaml:"user"`
	IdentityFile string // `yaml:"identity_file"`
}

// Networks is a list of user-defined networks
type Networks struct {
	Names []string
	nets  map[string]Network
}

func (n *Networks) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal(&n.nets)
	if err != nil {
		return err
	}

	n.reindex()

	return nil
}

func (n *Networks) reindex() {
	n.Names = make([]string, len(n.nets))
	i := 0
	for name := range n.nets {
		n.Names[i] = name
		i++
	}
}

func (n *Networks) Get(name string) (Network, bool) {
	net, ok := n.nets[name]
	return net, ok
}

// Command represents command(s) to be run remotely.
type Command struct {
	Name   string   `yaml:"-"`      // Command name.
	Desc   string   `yaml:"desc"`   // Command description.
	Local  string   `yaml:"local"`  // Command(s) to be run locally.
	Run    string   `yaml:"run"`    // Command(s) to be run remotely.
	Script string   `yaml:"script"` // Load command(s) from script and run it remotely.
	Upload []Upload `yaml:"upload"` // See Upload struct.
	Stdin  bool     `yaml:"stdin"`  // Attach localhost STDOUT to remote commands' STDIN?
	Once   bool     `yaml:"once"`   // The command should be run "once" (on one host only).
	Serial int      `yaml:"serial"` // Max number of clients processing a task in parallel.

	// API backward compatibility. Will be deprecated in v1.0.
	RunOnce bool `yaml:"run_once"` // The command should be run once only.
}

// Commands is a list of user-defined commands
type Commands struct {
	Names []string
	cmds  map[string]Command
}

func (c *Commands) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal(&c.cmds)
	if err != nil {
		return err
	}

	var items yaml.MapSlice
	err = unmarshal(&items)
	if err != nil {
		return err
	}

	c.reindex()

	return nil
}

func (c *Commands) reindex() {
	c.Names = make([]string, len(c.cmds))
	i := 0
	for name := range c.cmds {
		c.Names[i] = name
		i++
	}
}

func (c *Commands) Get(name string) (Command, bool) {
	cmd, ok := c.cmds[name]
	return cmd, ok
}

// Targets is a list of user-defined targets
type Targets struct {
	Names   []string
	targets map[string][]string
}

func (t *Targets) UnmarshalYAML(unmarshal func(interface{}) error) error {
	err := unmarshal(&t.targets)
	if err != nil {
		return err
	}

	t.reindex()

	return nil
}

func (t *Targets) reindex() {
	t.Names = make([]string, len(t.targets))
	i := 0
	for name := range t.targets {
		t.Names[i] = name
		i++
	}
}

func (t *Targets) Get(name string) ([]string, bool) {
	cmds, ok := t.targets[name]
	return cmds, ok
}

type Include struct {
	Supfile string   `yaml:"supfile"`
	Env     []string `yaml:"env"`
}

// Upload represents file copy operation from localhost Src path to Dst
// path of every host in a given Network.
type Upload struct {
	Src string `yaml:"src"`
	Dst string `yaml:"dst"`
	Exc string `yaml:"exclude"`
}

// EnvVar represents an environment variable
type EnvVar struct {
	Key   string
	Value string
}

func (e EnvVar) String() string {
	return e.Key + `=` + e.Value
}

// AsExport returns the environment variable as a bash export statement
func (e EnvVar) AsExport() string {
	return `export ` + e.Key + `="` + e.Value + `";`
}

// EnvList is a list of environment variables that maps to a YAML map,
// but maintains order, enabling late variables to reference early variables.
type EnvList []*EnvVar

func (e *EnvList) UnmarshalYAML(unmarshal func(interface{}) error) error {
	items := []yaml.MapItem{}

	err := unmarshal(&items)
	if err != nil {
		return err
	}

	*e = make(EnvList, 0, len(items))

	for _, v := range items {
		e.Set(fmt.Sprintf("%v", v.Key), fmt.Sprintf("%v", v.Value))
	}

	return nil
}

// Set key to be equal value in this list.
func (e *EnvList) Set(key, value string) {
	for i, v := range *e {
		if v.Key == key {
			(*e)[i].Value = value
			return
		}
	}

	*e = append(*e, &EnvVar{
		Key:   key,
		Value: value,
	})
}

// Get value by key.
func (e *EnvList) Get(key string) string {
	for _, v := range *e {
		if v.Key == key {
			return v.Value
		}
	}

	return ""
}

func (e *EnvList) ResolveValues() error {
	if len(*e) == 0 {
		return nil
	}

	exports := ""
	for i, v := range *e {
		exports += v.AsExport()

		cmd := exec.Command("bash", "-c", exports+"echo -n "+v.Value+";")
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		cmd.Dir = cwd
		resolvedValue, err := cmd.Output()
		if err != nil {
			return errors.Wrapf(err, "resolving env var %v failed", v.Key)
		}

		(*e)[i].Value = string(resolvedValue)
	}

	return nil
}

func (e *EnvList) AsExport() string {
	// Process all ENVs into a string of form
	// `export FOO="bar"; export BAR="baz";`.
	exports := ``
	for _, v := range *e {
		exports += v.AsExport() + " "
	}
	return exports
}

type ErrMustUpdate struct {
	Msg string
}

type ErrUnsupportedSupfileVersion struct {
	Msg string
}

func (e ErrMustUpdate) Error() string {
	return fmt.Sprintf("%v\n\nPlease update sup by `go get -u github.com/pressly/sup/cmd/sup`", e.Msg)
}

func (e ErrUnsupportedSupfileVersion) Error() string {
	return fmt.Sprintf("%v\n\nCheck your Supfile version (available latest version: v0.5)", e.Msg)
}

// NewSupfile parses configuration file and returns Supfile or error.
func NewSupfile(errStream io.Writer, data []byte) (*Supfile, error) {
	var conf Supfile

	if err := yaml.Unmarshal(data, &conf); err != nil {
		return nil, err
	}

	// API backward compatibility. Will be deprecated in v1.0.
	switch conf.Version {
	case "":
		conf.Version = "0.1"
		fallthrough

	case "0.1":
		for _, cmd := range conf.Commands.cmds {
			if cmd.RunOnce {
				return nil, ErrMustUpdate{"command.run_once is not supported in Supfile v" + conf.Version}
			}
		}
		fallthrough

	case "0.2":
		for _, cmd := range conf.Commands.cmds {
			if cmd.Once {
				return nil, ErrMustUpdate{"command.once is not supported in Supfile v" + conf.Version}
			}
			if cmd.Local != "" {
				return nil, ErrMustUpdate{"command.local is not supported in Supfile v" + conf.Version}
			}
			if cmd.Serial != 0 {
				return nil, ErrMustUpdate{"command.serial is not supported in Supfile v" + conf.Version}
			}
		}
		for _, network := range conf.Networks.nets {
			if network.Inventory != "" {
				return nil, ErrMustUpdate{"network.inventory is not supported in Supfile v" + conf.Version}
			}
		}
		fallthrough

	case "0.3":
		var warning string
		for key, cmd := range conf.Commands.cmds {
			if cmd.RunOnce {
				warning = "Warning: command.run_once was deprecated by command.once in Supfile v" + conf.Version + "\n"
				cmd.Once = true
				conf.Commands.cmds[key] = cmd
			}
		}
		if warning != "" {
			fmt.Fprintf(errStream, warning)
		}

		fallthrough

	case "0.4", "0.5":
		if len(conf.Includes) > 0 {
			return nil, ErrMustUpdate{"includes are not supported in Supfile v" + conf.Version}
		}

	case "0.6":

	default:
		return nil, ErrUnsupportedSupfileVersion{"unsupported Supfile version " + conf.Version}
	}

	return &conf, nil
}

// @TODO check version of the included file to be suported
func (conf *Supfile) ResolveIncludes(errStream io.Writer, reader func(filename string) ([]byte, error)) error {
	for _, include := range conf.Includes {
		data, err := reader(include.Supfile)
		if err != nil {
			return err
		}
		included, err := NewSupfile(errStream, data)
		if err != nil {
			return err
		}

		*conf = included.merge(conf, include.Env)
	}
	return nil
}

func (conf Supfile) merge(other *Supfile, injected []string ) Supfile {
	var combinedEnv EnvList
	for _, inj := range injected {
		combinedEnv.Set(inj, other.Env.Get(inj))
	}

	for _, env := range conf.Env {
		combinedEnv.Set(env.Key, env.Value)
	}
	for _, env := range other.Env {
		// adding injected again is a no-op, order is not altered
		combinedEnv.Set(env.Key, env.Value)
	}
	conf.Env = combinedEnv

	for name, network := range other.Networks.nets {
		conf.Networks.nets[name] = network
	}
	conf.Networks.reindex()

	for name, command := range other.Commands.cmds {
		conf.Commands.cmds[name] = command
	}
	conf.Commands.reindex()

	for name, target := range other.Targets.targets {
		conf.Targets.targets[name] = target
	}
	conf.Targets.reindex()

	return conf
}

// ParseInventory runs the inventory command, if provided, and appends
// the command's output lines to the manually defined list of hosts.
func (n Network) ParseInventory(errStream io.Writer) ([]string, error) {
	if n.Inventory == "" {
		return nil, nil
	}

	cmd := exec.Command("/bin/sh", "-c", n.Inventory)
	cmd.Stderr = errStream
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var hosts []string
	buf := bytes.NewBuffer(output)
	for {
		host, err := buf.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		host = strings.TrimSpace(host)
		// skip empty lines and comments
		if host == "" || host[:1] == "#" {
			continue
		}

		hosts = append(hosts, host)
	}
	return hosts, nil
}
