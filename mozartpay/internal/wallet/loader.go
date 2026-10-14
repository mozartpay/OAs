package wallet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/stellar/go/keypair"
)

// parseKeypairForAccount resolves the account's seed (plaintext or via a live
// passkey unlock grant) and parses it into a Stellar keypair. If expectedAddr
// is non-empty, the keypair's address must match it.
func parseKeypairForAccount(account *models.Account, expectedAddr string) (*keypair.Full, error) {
	seed, err := ResolveSeed(account)
	if err != nil {
		return nil, err
	}
	kp, err := keypair.ParseFull(seed)
	if err != nil {
		return nil, fmt.Errorf("invalid private key for wallet %s: %w", account.Address, err)
	}
	if expectedAddr != "" && kp.Address() != expectedAddr {
		return nil, fmt.Errorf("keypair address mismatch: expected %s, got %s", expectedAddr, kp.Address())
	}
	return kp, nil
}

// loadRegistryWallet reads the wallet file for address from the registry dir.
func loadRegistryWallet(address string) (*models.Account, error) {
	stateDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}
	walletPath := filepath.Join(stateDir, ".mozartpay", "state", "wallets", address+".json")
	data, err := os.ReadFile(walletPath)
	if err != nil {
		return nil, err
	}
	var account models.Account
	if err := json.Unmarshal(data, &account); err != nil {
		return nil, err
	}
	return &account, nil
}

// activeWalletAddress returns the active wallet address from the registry.
func activeWalletAddress() string {
	stateDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	registryPath := filepath.Join(stateDir, ".mozartpay", "state", "wallets.json")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return ""
	}
	var registry struct {
		ActiveWallet string `json:"activeWallet"`
	}
	if json.Unmarshal(data, &registry) != nil {
		return ""
	}
	return registry.ActiveWallet
}

// loadLegacyAccount reads an account from a legacy state file path.
func loadLegacyAccount(path string) *models.Account {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var account models.Account
	if err := json.Unmarshal(data, &account); err != nil {
		return nil
	}
	return &account
}

// LoadStellarKeypair loads the active Stellar keypair from the wallet registry or legacy paths.
// It tries multiple fallback locations in order:
// 1. Active wallet from registry (wallets.json -> wallets/<address>.json)
// 2. Legacy account_stellar.json
// 3. Legacy account.json (only if WalletStellar type)
// 4. Legacy stellar_keypair state
//
// All seed resolution goes through ResolveSeed, so a passkey-protected wallet
// must be unlocked (mozartpay wallet passkey unlock) before it can sign.
func LoadStellarKeypair() (*keypair.Full, error) {
	stateDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Try to load from wallet registry first
	if active := activeWalletAddress(); active != "" {
		if account, err := loadRegistryWallet(active); err == nil {
			if kp, err := parseKeypairForAccount(account, account.Address); err == nil {
				return kp, nil
			} else if account.PrivateKey != "" || account.PasskeyProtected {
				return nil, err
			}
		}
	}

	// Fall back to legacy paths for compatibility
	stellarAccountPath := filepath.Join(stateDir, ".mozartpay", "state", "account_stellar.json")
	if account := loadLegacyAccount(stellarAccountPath); account != nil {
		if kp, err := parseKeypairForAccount(account, account.Address); err == nil {
			return kp, nil
		} else if account.PrivateKey != "" || account.PasskeyProtected {
			return nil, err
		}
	}

	defaultAccountPath := filepath.Join(stateDir, ".mozartpay", "state", "account.json")
	if account := loadLegacyAccount(defaultAccountPath); account != nil && account.Type == models.WalletStellar {
		if kp, err := parseKeypairForAccount(account, account.Address); err == nil {
			return kp, nil
		} else if account.PrivateKey != "" || account.PasskeyProtected {
			return nil, err
		}
	}

	// Final fallback to old format
	var saved struct {
		Seed string `json:"seed"`
	}
	if err := config.LoadState("stellar_keypair", &saved); err == nil && saved.Seed != "" {
		return keypair.ParseFull(saved.Seed)
	}

	return nil, fmt.Errorf("no Stellar keypair found")
}

// LoadStellarKeypairForSwap loads a Stellar keypair for swap operations.
// Passkey-protected wallets are supported but must be unlocked first.
func LoadStellarKeypairForSwap() (*keypair.Full, error) {
	stateDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// Load registry to get active wallet
	if active := activeWalletAddress(); active != "" {
		if account, err := loadRegistryWallet(active); err == nil {
			if kp, err := parseKeypairForAccount(account, account.Address); err == nil {
				return kp, nil
			} else if account.PrivateKey != "" || account.PasskeyProtected {
				return nil, err
			}
		}
	}

	// Fall back to legacy paths for compatibility
	stellarAccountPath := filepath.Join(stateDir, ".mozartpay", "state", "account_stellar.json")
	if account := loadLegacyAccount(stellarAccountPath); account != nil {
		if kp, err := parseKeypairForAccount(account, account.Address); err == nil {
			return kp, nil
		} else if account.PrivateKey != "" || account.PasskeyProtected {
			return nil, err
		}
	}

	return nil, fmt.Errorf("no Stellar keypair found")
}

// LoadStellarKeypairForAddress loads a specific Stellar keypair by address from the wallet registry.
// This ensures the correct wallet is used when cfg.ActiveAddress may differ from the registry's active wallet.
// The keypair's address is verified against the requested address; passkey-protected
// wallets must be unlocked first.
func LoadStellarKeypairForAddress(address string) (*keypair.Full, error) {
	if account, err := loadRegistryWallet(address); err == nil {
		return parseKeypairForAccount(account, address)
	}

	return nil, fmt.Errorf("no Stellar keypair found for address: %s", address)
}
