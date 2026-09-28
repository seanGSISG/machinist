package cli

import (
	"encoding/json"
	"fmt"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/spf13/cobra"
)

type configValidationError struct{}

func (*configValidationError) Error() string { return "configuration is invalid" }

type configValidationResult struct {
	Valid           bool                    `json:"valid"`
	InvalidCommands []config.InvalidCommand `json:"invalid_commands"`
	Error           *string                 `json:"error"`
}

func newConfigCommand(options *commandOptions) *cobra.Command {
	group := &cobra.Command{Use: "config", Short: "Inspect Machinist configuration"}
	group.AddCommand(newConfigValidateCommand(options))
	return group
}

func newConfigValidateCommand(options *commandOptions) *cobra.Command {
	jsonOutput := false
	command := &cobra.Command{
		Use:   "validate",
		Short: "Validate the control plane configuration",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			report, validationErr := config.ValidateFile(options.configPath)
			result := configValidationResult{
				Valid:           validationErr == nil && len(report.InvalidCommands) == 0,
				InvalidCommands: report.InvalidCommands,
			}
			if result.InvalidCommands == nil {
				result.InvalidCommands = []config.InvalidCommand{}
			}
			if validationErr != nil {
				message := validationErr.Error()
				result.Error = &message
			}
			if jsonOutput {
				if err := json.NewEncoder(options.stdout).Encode(result); err != nil {
					return err
				}
			} else if result.Valid {
				if _, err := fmt.Fprintln(options.stdout, "configuration is valid"); err != nil {
					return err
				}
			} else {
				if _, err := fmt.Fprintln(options.stdout, "configuration is invalid"); err != nil {
					return err
				}
				if validationErr != nil {
					if _, err := fmt.Fprintf(options.stdout, "- %s\n", validationErr); err != nil {
						return err
					}
				}
				for _, invalid := range report.InvalidCommands {
					if _, err := fmt.Fprintf(options.stdout, "- %s: %s\n", invalid.Name, invalid.Reason); err != nil {
						return err
					}
				}
			}
			if !result.Valid {
				return &configValidationError{}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&jsonOutput, "json", false, "print validation results as JSON")
	return command
}
