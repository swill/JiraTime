package main

import (
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

// IsSuperUser reports whether a session belongs to a configured super user.
// SUPER_USERS lists sign-in emails, matched case-insensitively against the
// account's email as Atlassian's identity service reports it, and only once
// Atlassian reports that email verified: an address the user hasn't proven
// they own grants nothing.
func IsSuperUser(session *UserSession) bool {
	email := strings.ToLower(strings.TrimSpace(session.Email))
	if email == "" {
		return false
	}
	for _, su := range viper.GetStringSlice("SUPER_USERS") {
		if strings.ToLower(strings.TrimSpace(su)) != email {
			continue
		}
		if !session.EmailVerified {
			warnOnce("unverified/"+session.AccountID, "Account %s's email matches SUPER_USERS, but Atlassian doesn't report it as verified, so super user access is refused", session.AccountID)
			return false
		}
		return true
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
	viper.SetDefault("ACTIVE_ISSUES_WEEKS", 4) // How many weeks back to look for active issues
	viper.SetDefault("DONE_ISSUES_DAYS", 90)   // How many days Done issues stay available to log time against

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

	// Super users used to be listed by account ID; those entries now match nobody
	for _, su := range viper.GetStringSlice("SUPER_USERS") {
		if !strings.Contains(su, "@") {
			logrus.Warnf("SUPER_USERS entry %q isn't an email address and is ignored: super users are matched by their Atlassian sign-in email", su)
		}
	}

	if viper.IsSet("DONE_ISSUES_WEEKS") {
		logrus.Warnf("DONE_ISSUES_WEEKS is no longer used and is ignored - set DONE_ISSUES_DAYS instead (currently %d)", viper.GetInt("DONE_ISSUES_DAYS"))
	}
}
