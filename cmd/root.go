package cmd

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/azkazamdigital/wa-gateway/config"
	"github.com/azkazamdigital/wa-gateway/internal/ai"
	"github.com/azkazamdigital/wa-gateway/internal/auth"
	"github.com/azkazamdigital/wa-gateway/internal/storage"
	tenantpkg "github.com/azkazamdigital/wa-gateway/internal/tenant"
	"github.com/azkazamdigital/wa-gateway/pkg/utils"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	EmbedViews    embed.FS
	Store         *storage.Storage
	AuthService   *auth.Service
	TenantManager *tenantpkg.Manager
)

var rootCmd = &cobra.Command{
	Use:   "wa-gateway",
	Short: "InstaBlast Pro - WhatsApp Broadcast API",
	Long:  "InstaBlast Pro with broadcast, personalization, AI, and multi-account management",
	Run:   runServer,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&config.AppPort, "port", "p", config.AppPort, "Server port")
	rootCmd.PersistentFlags().StringVarP(&config.AppHost, "host", "H", config.AppHost, "Server host")
	rootCmd.PersistentFlags().BoolVarP(&config.AppDebug, "debug", "d", config.AppDebug, "Enable debug mode")
}

func Execute(embedViews embed.FS) {
	EmbedViews = embedViews
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func initApp() {
	if config.AppDebug {
		config.WhatsappLogLevel = "DEBUG"
		logrus.SetLevel(logrus.DebugLevel)
	}

	if err := utils.CreateFolder(config.PathQrCode, config.PathSendItems, config.PathStorages, config.PathMedia); err != nil {
		logrus.Errorln(err)
	}

	var err error
	Store, err = storage.New(config.PathStorages + "/app.db")
	if err != nil {
		logrus.Fatalf("Failed to initialize app storage: %v", err)
	}
	if err := Store.EnsureUserTable(); err != nil {
		logrus.Fatalf("Failed to initialize user table: %v", err)
	}
	if err := Store.EnsureTrialSignupTable(); err != nil {
		logrus.Fatalf("Failed to initialize trial signup table: %v", err)
	}
	if err := Store.EnsureTrialOTPSessionTable(); err != nil {
		logrus.Fatalf("Failed to initialize trial otp session table: %v", err)
	}
	if err := Store.SeedAdminUser("azam@gmail.com", "Nr201105"); err != nil {
		logrus.Fatalf("Failed to seed admin user: %v", err)
	}
	AuthService = auth.NewService(Store)
	initTrialOTPVerifierManager()
	ai.SetGlobalAPIKeyProvider(func() string {
		return Store.GetPref("global_nvidia_api_key")
	})
	TenantManager = tenantpkg.NewManager(Store, filepath.Join(config.PathStorages, "tenants"), func(msg, level string) {
		broadcastWSLog(msg, level)
	})
	go TenantManager.StartActiveUsers(context.Background())

	fmt.Printf("InstaBlast Pro v%s starting...\n", config.AppVersion)
}
