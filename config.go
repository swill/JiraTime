package main

import (
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// Default custom time fields - these track time in minutes on issues
var defaultCustomTimeFields = []CustomTimeField{
	{ID: "customfield_11710", Label: "Billable Time"},
	{ID: "customfield_11712", Label: "Smart Hands and Eyes"},
	{ID: "customfield_12073", Label: "Smart Hands and Eyes (After Hours)"},
}

// GetCustomTimeFields returns the configured custom time fields
func GetCustomTimeFields() []CustomTimeField {
	return defaultCustomTimeFields
}

// IsSuperUser checks if the given account ID is configured as a super user
func IsSuperUser(accountID string) bool {
	superUsers := viper.GetStringSlice("SUPER_USERS")
	for _, su := range superUsers {
		if su == accountID {
			return true
		}
	}
	return false
}

func initConfig() {
	viper.SetConfigName("config")
	viper.SetConfigType("toml")
	viper.AddConfigPath(".")

	// Set defaults
	viper.SetDefault("PORT", 8080)
	viper.SetDefault("HOURS_TARGET", 40)
	viper.SetDefault("ACTIVE_ISSUES_WEEKS", 4)  // How many weeks back to look for active issues
	viper.SetDefault("DONE_ISSUES_WEEKS", 2)    // How many weeks Done issues stay visible

	// Environment variables override config file
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			logrus.Fatal("config.toml not found - copy empty_config.toml to config.toml and fill in values")
		}
		logrus.Fatalf("error reading config: %v", err)
	}

	// Validate required keys
	required := []string{
		"JIRA_CLIENT_ID",
		"JIRA_CLIENT_SECRET",
		"BASE_URL",
		"SESSION_SECRET",
	}

	for _, key := range required {
		if viper.GetString(key) == "" {
			logrus.Fatalf("required config key %s is not set", key)
		}
	}

	// Validate SESSION_SECRET length
	if len(viper.GetString("SESSION_SECRET")) < 32 {
		logrus.Fatal("SESSION_SECRET must be at least 32 characters")
	}
}
