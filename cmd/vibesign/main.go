package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
)

const (
	envSignerURL     = "VIBESIGN_URL"
	envPubKeyURL     = "VIBESIGN_PUBKEY_URL"
	envSignerToken   = "VIBESIGN_TOKEN"
	envSignerTimeout = "VIBESIGN_TIMEOUT"
	defaultTimeout   = 10 * time.Second
)

type signerArgs struct {
	operation   string
	namespace   string
	keyFile     string
	payloadFile string
	useAgent    bool
	options     []string
}

type signRequest struct {
	Namespace string   `json:"namespace"`
	Payload   string   `json:"payload"`
	UseAgent  bool     `json:"use_agent,omitempty"`
	Options   []string `json:"options,omitempty"`
}

type signResponse struct {
	Signature string `json:"signature"`
}

type publicKeyResponse struct {
	PublicKey string `json:"public_key"`
}

func main() {
	if err := execute(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	cmd := newRootCommand(getenv, stdout, stderr)
	return cmd.Run(context.Background(), append([]string{"vibesign"}, args...))
}

func newRootCommand(getenv func(string) string, stdout, stderr io.Writer) *cli.Command {
	return &cli.Command{
		Name:            "vibesign",
		Usage:           "Git SSH signing helper for Vibepit sandboxes",
		HideHelp:        true,
		HideHelpCommand: true,
		Writer:          stdout,
		ErrWriter:       stderr,
		Flags:           signFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			parsed := signArgsFromCommand(cmd)
			if err := validateSignArgs(parsed, cmd.Args().Slice()); err != nil {
				return err
			}
			return runSignParsed(parsed, getenv)
		},
		Commands: []*cli.Command{
			newPubKeyCommand(getenv, stdout, stderr),
		},
	}
}

func newPubKeyCommand(getenv func(string) string, stdout, stderr io.Writer) *cli.Command {
	return &cli.Command{
		Name:            "pubkey",
		Usage:           "Fetch the SSH public key for Git defaultKeyCommand",
		HideHelp:        true,
		HideHelpCommand: true,
		Writer:          stdout,
		ErrWriter:       stderr,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.NArg() != 0 {
				return errors.New("pubkey does not accept arguments")
			}
			return runPubKey(getenv, stdout)
		},
	}
}

func runSignParsed(parsed *signerArgs, getenv func(string) string) error {
	if parsed.operation != "sign" {
		return fmt.Errorf("unsupported ssh signing operation %q", parsed.operation)
	}

	url := strings.TrimSpace(getenv(envSignerURL))
	if url == "" {
		return fmt.Errorf("%s is required", envSignerURL)
	}

	timeout, err := loadTimeout(getenv(envSignerTimeout))
	if err != nil {
		return err
	}

	payload, err := os.ReadFile(parsed.payloadFile)
	if err != nil {
		return fmt.Errorf("read payload %q: %w", parsed.payloadFile, err)
	}

	signature, err := requestSignature(http.DefaultClient, url, payload, parsed, strings.TrimSpace(getenv(envSignerToken)), timeout)
	if err != nil {
		return err
	}

	if err := writeSignatureFile(parsed.payloadFile+".sig", signature); err != nil {
		return err
	}
	return nil
}

func newSignCommand() *cli.Command {
	return &cli.Command{
		Name:  "vibesign",
		Flags: signFlags(),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return nil
		},
	}
}

func signFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "operation",
			Aliases: []string{"Y"},
			Hidden:  true,
		},
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Hidden:  true,
		},
		&cli.StringFlag{
			Name:    "key-file",
			Aliases: []string{"f"},
			Hidden:  true,
		},
		&cli.StringSliceFlag{
			Name:    "option",
			Aliases: []string{"O"},
			Hidden:  true,
		},
		&cli.BoolFlag{
			Name:    "use-agent",
			Aliases: []string{"U"},
			Hidden:  true,
		},
	}
}

func runPubKey(getenv func(string) string, stdout io.Writer) error {
	url := strings.TrimSpace(getenv(envPubKeyURL))
	if url == "" {
		return fmt.Errorf("%s is required", envPubKeyURL)
	}

	timeout, err := loadTimeout(getenv(envSignerTimeout))
	if err != nil {
		return err
	}

	publicKey, err := requestPublicKey(http.DefaultClient, url, strings.TrimSpace(getenv(envSignerToken)), timeout)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "key::%s\n", publicKey); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}

	return nil
}

func signArgsFromCommand(cmd *cli.Command) *signerArgs {
	return &signerArgs{
		operation:   cmd.String("operation"),
		namespace:   cmd.String("namespace"),
		keyFile:     cmd.String("key-file"),
		payloadFile: firstArg(cmd.Args().Slice()),
		useAgent:    cmd.Bool("use-agent"),
		options:     cmd.StringSlice("option"),
	}
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func validateSignArgs(parsed *signerArgs, extraArgs []string) error {
	switch {
	case parsed.operation == "":
		return errors.New("missing -Y operation")
	case parsed.namespace == "":
		return errors.New("missing -n namespace")
	case parsed.keyFile == "":
		return errors.New("missing -f key file")
	case parsed.payloadFile == "":
		return errors.New("missing payload file")
	case len(extraArgs) > 1:
		return errors.New("multiple payload files provided")
	}

	return nil
}

func loadTimeout(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultTimeout, nil
	}

	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", envSignerTimeout, err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("%s must be positive", envSignerTimeout)
	}
	return timeout, nil
}

func requestSignature(client *http.Client, url string, payload []byte, parsed *signerArgs, token string, timeout time.Duration) (string, error) {
	requestBody := signRequest{
		Namespace: parsed.namespace,
		Payload:   base64.StdEncoding.EncodeToString(payload),
		UseAgent:  parsed.useAgent,
		Options:   parsed.options,
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("marshal sign request: %w", err)
	}

	httpClient := *client
	httpClient.Timeout = timeout

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build sign request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sign request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read sign response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sign request failed: %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}

	signature, err := decodeSignature(resp.Header.Get("Content-Type"), responseBody)
	if err != nil {
		return "", err
	}

	if !looksLikeSSHSignature(signature) {
		return "", errors.New("signer returned an invalid SSH signature")
	}

	return signature, nil
}

func requestPublicKey(client *http.Client, url, token string, timeout time.Duration) (string, error) {
	httpClient := *client
	httpClient.Timeout = timeout

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build pubkey request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/plain")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("pubkey request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read pubkey response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pubkey request failed: %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}

	publicKey, err := decodePublicKey(resp.Header.Get("Content-Type"), responseBody)
	if err != nil {
		return "", err
	}
	if !looksLikeSSHPublicKey(publicKey) {
		return "", errors.New("signer returned an invalid SSH public key")
	}

	return publicKey, nil
}

func decodeSignature(contentType string, body []byte) (string, error) {
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		var response signResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return "", fmt.Errorf("decode JSON sign response: %w", err)
		}
		return strings.TrimSpace(response.Signature), nil
	}
	return strings.TrimSpace(string(body)), nil
}

func decodePublicKey(contentType string, body []byte) (string, error) {
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		var response publicKeyResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return "", fmt.Errorf("decode JSON pubkey response: %w", err)
		}
		return trimKeyPrefix(response.PublicKey), nil
	}
	return trimKeyPrefix(string(body)), nil
}

func looksLikeSSHSignature(signature string) bool {
	return strings.HasPrefix(signature, "-----BEGIN SSH SIGNATURE-----") &&
		strings.HasSuffix(signature, "-----END SSH SIGNATURE-----")
}

func looksLikeSSHPublicKey(publicKey string) bool {
	fields := strings.Fields(publicKey)
	if len(fields) < 2 {
		return false
	}
	return strings.HasPrefix(fields[0], "ssh-") || strings.HasPrefix(fields[0], "ecdsa-") || strings.HasPrefix(fields[0], "sk-")
}

func trimKeyPrefix(value string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "key::"))
}

func writeSignatureFile(path, signature string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vibesign-*.sig")
	if err != nil {
		return fmt.Errorf("create temp signature file: %w", err)
	}

	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp signature file: %w", err)
	}
	if _, err := tmp.WriteString(signature + "\n"); err != nil {
		return fmt.Errorf("write temp signature file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp signature file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename signature file: %w", err)
	}

	cleanup = false
	return nil
}
