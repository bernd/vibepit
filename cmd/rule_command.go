package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/bernd/vibepit/tui"
	"github.com/urfave/cli/v3"
)

// ruleAction is what a rule command adds to the running proxy.
type ruleAction int

const (
	ruleAllow ruleAction = iota
	ruleDeny
)

func (a ruleAction) String() string {
	if a == ruleDeny {
		return "deny"
	}
	return "allow"
}

// funcs returns how entries of source are added to the running proxy and
// saved to the project config.
func (a ruleAction) funcs(client *ControlClient, source proxy.Source) (live func([]string) ([]string, error), persist func(string, []string) error) {
	switch {
	case a == ruleAllow && source == proxy.SourceDNS:
		return client.AllowDNS, config.AppendAllowDNS
	case a == ruleAllow:
		return client.AllowHTTP, config.AppendAllowHTTP
	case source == proxy.SourceDNS:
		return client.DenyDNS, config.AppendDenyDNS
	default:
		return client.DenyHTTP, config.AppendDenyHTTP
	}
}

func AllowHTTPCommand() *cli.Command {
	return ruleCommand(ruleAllow, proxy.SourceProxy, discoverSession, NewControlClient)
}

func AllowDNSCommand() *cli.Command {
	return ruleCommand(ruleAllow, proxy.SourceDNS, discoverSession, NewControlClient)
}

func DenyHTTPCommand() *cli.Command {
	return ruleCommand(ruleDeny, proxy.SourceProxy, discoverSession, NewControlClient)
}

func DenyDNSCommand() *cli.Command {
	return ruleCommand(ruleDeny, proxy.SourceDNS, discoverSession, NewControlClient)
}

// ruleCommand builds allow-http, allow-dns, deny-http and deny-dns: validate,
// find the session, add the entries to the running proxy, then save them
// unless --no-save is given.
func ruleCommand(action ruleAction, source proxy.Source, discover func(context.Context, string) (*SessionInfo, error), connect func(*SessionInfo) (*ControlClient, error)) *cli.Command {
	protocol, args, validate := "http", "<domain:port-pattern>...", proxy.ValidateHTTPEntries
	if source == proxy.SourceDNS {
		protocol, args, validate = "dns", "<domain-pattern>...", proxy.ValidateDNSEntries
	}
	name := action.String() + "-" + protocol
	usage := "Add entries to the proxy " + strings.ToUpper(protocol) + " allowlist"
	label := "Allowed"
	if action == ruleDeny {
		usage = "Add enforced entries to the proxy " + strings.ToUpper(protocol) + " denylist"
		label = "Denied"
	}
	return &cli.Command{
		Name:      name,
		Usage:     usage,
		ArgsUsage: args,
		Flags:     []cli.Flag{&cli.BoolFlag{Name: "no-save", Usage: "Skip persisting to project config"}, sessionFlag},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			entries := cmd.Args().Slice()
			if len(entries) == 0 {
				return cli.ShowSubcommandHelp(cmd)
			}
			if err := validate(entries); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			session, err := discover(ctx, cmd.String("session"))
			if err != nil {
				return fmt.Errorf("cannot find running proxy: %w", err)
			}
			client, err := connect(session)
			if err != nil {
				return err
			}
			save := !cmd.Bool("no-save")
			live, persist := action.funcs(client, source)
			applied, err := applyRule(live, persist, session, entries, save)
			if applied {
				for _, entry := range entries {
					tui.Status(label, "%s", entry)
				}
			}
			if err != nil {
				if applied {
					return unsavedError(action, err)
				}
				return err
			}
			if save {
				tui.Status("Saved", "to %s", config.DefaultProjectPath(session.ProjectDir))
			}
			return nil
		},
	}
}

// applyRule adds entries to the running proxy, then persists them when save
// is set. applied reports the live change: it stays true when only the save
// failed, so callers can say the rule is active for this session.
func applyRule(live func([]string) ([]string, error), persist func(string, []string) error, session *SessionInfo, entries []string, save bool) (applied bool, err error) {
	if _, err := live(entries); err != nil {
		return false, err
	}
	if save {
		if err := persist(config.DefaultProjectPath(session.ProjectDir), entries); err != nil {
			return true, err
		}
	}
	return true, nil
}

// allowEntry adds k's target to the running proxy's allowlist and, when
// save is set, persists it to the project config. Shared by the monitor
// and approve screens. A failed save returns statusTemp with the error:
// the proxy allows the target for this session all the same.
func allowEntry(client *ControlClient, session *SessionInfo, k proxy.HostKey, save bool) (allowStatus, error) {
	live, persist := ruleAllow.funcs(client, k.Source)
	applied, err := applyRule(live, persist, session, []string{k.Target().String()}, save)
	switch {
	case !applied:
		return statusNone, err
	case !save || err != nil:
		return statusTemp, err
	}
	return statusSaved, nil
}

// denyEntries installs enforced rules before persisting them. applied remains
// true when saving fails, so callers can report the live rule accurately.
func denyEntries(client *ControlClient, session *SessionInfo, source proxy.Source, entries []string, save bool) (applied bool, err error) {
	live, persist := ruleDeny.funcs(client, source)
	applied, err = applyRule(live, persist, session, entries, save)
	if applied && err != nil {
		err = unsavedError(ruleDeny, err)
	}
	return applied, err
}

// unsavedError reports a save that failed after the rule was applied, so the
// user knows it is active until the proxy stops.
func unsavedError(action ruleAction, err error) error {
	return fmt.Errorf("%s applied and remains active in running proxy, but saving config failed: %w", action, err)
}
