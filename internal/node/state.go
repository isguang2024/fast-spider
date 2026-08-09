package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type State struct {
	HubURL         string `json:"hubUrl"`
	MachineID      string `json:"machineId"`
	CredentialID   string `json:"credentialId"`
	HubPublicKey   string `json:"hubPublicKey"`
	HubFingerprint string `json:"hubFingerprint"`
}

func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, ErrNotRegistered
		}
		return State{}, fmt.Errorf("read node state: %w", err)
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("decode node state: %w", err)
	}
	if state.HubURL == "" || state.MachineID == "" || state.HubPublicKey == "" || state.HubFingerprint == "" {
		return State{}, fmt.Errorf("node state is incomplete")
	}
	return state, nil
}

func SaveState(path string, state State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
