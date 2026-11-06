package auth

import (
	"fmt"
	"log/slog"
	"os"
)

// DefaultAdminPassword is the password the root account gets on first run when
// MUNINN_ADMIN_PASSWORD is unset. It is only safe while the server is bound to
// a loopback address.
const DefaultAdminPassword = "password"

// Bootstrap ensures an admin user, session secret, and default vault config exist.
// On first run, creates "root" with the default password "password" (or the value
// of MUNINN_ADMIN_PASSWORD if set), sets the "default" vault to public (no API key
// required), and prints a reminder to change the password.
// On subsequent runs, if MUNINN_ADMIN_PASSWORD is set it is applied to the existing
// "root" account so that containerised/automated deployments can rotate the password
// via environment variable without a first-run wipe.
// secretPath is where the session signing secret is persisted (e.g. dataDir/auth_secret).
// exposed says whether the server is reachable from other hosts. When it is, the
// default password is refused and the bootstrap vault stays private.
func Bootstrap(store *Store, secretPath string, exposed bool) (secret []byte, err error) {
	// Load or generate session secret
	secret, err = os.ReadFile(secretPath)
	if err != nil {
		secret, err = GenerateSecret()
		if err != nil {
			return nil, fmt.Errorf("generate session secret: %w", err)
		}
		if writeErr := os.WriteFile(secretPath, secret, 0600); writeErr != nil {
			return nil, fmt.Errorf("write session secret: %w", writeErr)
		}
		slog.Info("generated new session secret", "path", secretPath)
	}

	envPassword := os.Getenv("MUNINN_ADMIN_PASSWORD")

	// The bootstrap vault is public only while the server stays on the local
	// machine. MUNINN_DEFAULT_VAULT_PUBLIC=1 opts back in for deployments where
	// something in front of muninn authenticates every request (a reverse proxy
	// with a shared secret, for example).
	defaultVaultPublic := os.Getenv("MUNINN_DEFAULT_VAULT_PUBLIC") == "1"
	public := !exposed || defaultVaultPublic
	if defaultVaultPublic && exposed {
		slog.Warn("MUNINN_DEFAULT_VAULT_PUBLIC=1: the default vault is public while the server is reachable from other hosts")
	}

	// Create root admin if none exists
	if !store.AdminExists() {
		adminPassword := envPassword
		if adminPassword == "" {
			adminPassword = DefaultAdminPassword
		}
		if exposed && adminPassword == DefaultAdminPassword {
			return nil, fmt.Errorf("refusing to start: the server is reachable from other hosts but the admin " +
				"account would use the default password; set MUNINN_ADMIN_PASSWORD or bind to 127.0.0.1")
		}
		if err = store.CreateAdmin("root", adminPassword); err != nil {
			return nil, fmt.Errorf("create root admin: %w", err)
		}

		fmt.Println("┌──────────────────────────────────────────────────┐")
		fmt.Println("│            MuninnDB — First Run Setup             │")
		fmt.Println("│                                                    │")
		fmt.Println("│  Admin username : root                             │")
		if envPassword != "" {
			fmt.Println("│  Admin password : (set via MUNINN_ADMIN_PASSWORD)  │")
		} else {
			fmt.Println("│  Admin password : password                         │")
		}
		fmt.Println("│                                                    │")
		if public {
			fmt.Println("│  Default vault  : public (no API key required)     │")
		} else {
			fmt.Println("│  Default vault  : private (API key required)       │")
		}
		if envPassword == "" {
			fmt.Println("│                                                    │")
			fmt.Println("│  Change your password and review vault settings    │")
			fmt.Println("│  in the admin UI before exposing to a network.     │")
		}
		fmt.Println("└──────────────────────────────────────────────────┘")
	} else if envPassword != "" {
		// Existing instance: MUNINN_ADMIN_PASSWORD is set — update the root password
		// so operators can rotate credentials via environment variable.
		if changeErr := store.ChangeAdminPassword("root", envPassword); changeErr != nil {
			slog.Warn("failed to apply MUNINN_ADMIN_PASSWORD to existing root account",
				"err", changeErr)
		} else {
			slog.Info("root admin password updated from MUNINN_ADMIN_PASSWORD")
		}
	}

	// Fail fast when the server is reachable from other hosts while the admin
	// account still uses the default password. Starting in that state would
	// hand the server to anyone who can reach the port.
	if exposed && store.ValidateAdmin("root", DefaultAdminPassword) == nil {
		return nil, fmt.Errorf("refusing to start: the admin account still uses the default password; " +
			"set MUNINN_ADMIN_PASSWORD or change it in the admin UI")
	}

	// Ensure at least one vault config exists. Covers both fresh installs
	// and upgrades from versions that didn't create vault configs during
	// bootstrap. Without this, fail-closed mode locks out all MCP clients.
	cfgs, cfgErr := store.ListVaultConfigs()
	if cfgErr == nil && len(cfgs) == 0 {
		if err = store.SetVaultConfig(VaultConfig{Name: "default", Public: public}); err != nil {
			return nil, fmt.Errorf("configure default vault: %w", err)
		}
		slog.Info("created default vault config", "public", public)
	}

	return secret, nil
}
