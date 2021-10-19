package runner

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/projectdiscovery/fileutil"
	"github.com/projectdiscovery/gologger"
	"github.com/projectdiscovery/gologger/formatter"
	"github.com/projectdiscovery/gologger/levels"
	"github.com/projectdiscovery/nuclei/v2/pkg/catalog/config"
	"github.com/projectdiscovery/nuclei/v2/pkg/protocols/common/protocolinit"
	"github.com/projectdiscovery/nuclei/v2/pkg/types"
)

// ParseOptions parses the command line flags provided by a user
func ParseOptions(options *types.Options) {
	// Check if stdin pipe was given
	options.Stdin = hasStdin()

	// if VerboseVerbose is set, it implicitly enables the Verbose option as well
	if options.VerboseVerbose {
		options.Verbose = true
	}

	// Read the inputs and configure the logging
	configureOutput(options)
	// Show the user the banner
	showBanner()

	if options.Version {
		gologger.Info().Msgf("Current Version: %s\n", config.Version)
		os.Exit(0)
	}
	if options.TemplatesVersion {
		configuration, err := config.ReadConfiguration()
		if err != nil {
			gologger.Fatal().Msgf("Could not read template configuration: %s\n", err)
		}
		gologger.Info().Msgf("Current nuclei-templates version: %s (%s)\n", configuration.TemplateVersion, configuration.TemplatesDirectory)
		os.Exit(0)
	}

	// Validate the options passed by the user and if any
	// invalid options have been used, exit.
	if err := validateOptions(options); err != nil {
		gologger.Fatal().Msgf("Program exiting: %s\n", err)
	}

	// Auto adjust rate limits when using headless mode if the user
	// hasn't specified any custom limits.
	if options.Headless && options.BulkSize == 25 && options.TemplateThreads == 10 {
		options.BulkSize = 2
		options.TemplateThreads = 2
	}

	// Load the resolvers if user asked for them
	loadResolvers(options)

	// removes all cli variables containing payloads and add them to the internal struct
	for key, value := range options.Vars.AsMap() {
		if fileutil.FileExists(value.(string)) {
			_ = options.Vars.Del(key)
			options.AddVarPayload(key, value)
		}
	}

	err := protocolinit.Init(options)
	if err != nil {
		gologger.Fatal().Msgf("Could not initialize protocols: %s\n", err)
	}
}

// hasStdin returns true if we have stdin input
func hasStdin() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}

	isPipedFromChrDev := (stat.Mode() & os.ModeCharDevice) == 0
	isPipedFromFIFO := (stat.Mode() & os.ModeNamedPipe) != 0

	return isPipedFromChrDev || isPipedFromFIFO
}

// validateOptions validates the configuration options passed
func validateOptions(options *types.Options) error {
	if options.Verbose && options.Silent {
		return errors.New("both verbose and silent mode specified")
	}
	//loading the proxy server list and test the connectivity
	loadProxies(options)
	if options.Validate {
		options.Headless = true // required for correct validation of headless templates
		validateTemplatePaths(options.TemplatesDirectory, options.Templates, options.Workflows)
	}

	return nil
}
func validateProxy(proxy string) error {
	if err := validateProxyURL(proxy, "invalid http proxy format (It should be http://username:password@host:port)"); err != nil {
		return err
	}
	if err := validateProxyURL(proxy, "invalid socks proxy format (It should be socks5://username:password@host:port)"); err != nil {
		return err
	}
	return nil
}

func validateProxyURL(proxyURL, message string) error {
	if proxyURL != "" && !isValidURL(proxyURL) {
		return errors.New(message)
	}

	return nil
}

func isValidURL(urlString string) bool {
	_, err := url.Parse(urlString)
	return err == nil
}

// configureOutput configures the output logging levels to be displayed on the screen
func configureOutput(options *types.Options) {
	// If the user desires verbose output, show verbose output
	if options.Verbose || options.VerboseVerbose {
		gologger.DefaultLogger.SetMaxLevel(levels.LevelVerbose)
	}
	if options.Debug {
		gologger.DefaultLogger.SetMaxLevel(levels.LevelDebug)
	}
	if options.NoColor {
		gologger.DefaultLogger.SetFormatter(formatter.NewCLI(true))
	}
	if options.Silent {
		gologger.DefaultLogger.SetMaxLevel(levels.LevelSilent)
	}
}

// loadResolvers loads resolvers from both user provided flag and file
func loadResolvers(options *types.Options) {
	if options.ResolversFile == "" {
		return
	}

	file, err := os.Open(options.ResolversFile)
	if err != nil {
		gologger.Fatal().Msgf("Could not open resolvers file: %s\n", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		part := scanner.Text()
		if part == "" {
			continue
		}
		if strings.Contains(part, ":") {
			options.InternalResolversList = append(options.InternalResolversList, part)
		} else {
			options.InternalResolversList = append(options.InternalResolversList, part+":53")
		}
	}
}

// loadProxies load list of proxy servers from file
func loadProxies(options *types.Options) {
	if options.Proxy == "" {
		return
	}
	file, err := os.Open(options.Proxy)
	if err != nil {
		gologger.Fatal().Msgf("Could not open proxy file: %s\n", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		proxy := scanner.Text()
		if proxy == "" {
			continue
		}
		if err := validateProxy(proxy); err != nil {
			gologger.Fatal().Msgf("%s\n", err)
		}
		options.ProxyURLList = append(options.ProxyURLList, proxy)
	}
	if len(options.ProxyURLList) == 0 {
		gologger.Fatal().Msgf("Could not find any proxy in the file\n")
	} else {
		done := make(chan bool)
		for _, ip := range options.ProxyURLList {
			go runProxyConnectivity(ip, options, done)
		}
		<-done
		close(done)
	}
}
func runProxyConnectivity(ip string, options *types.Options, done chan bool) {
	if proxy, err := testProxyConnection(ip); err == nil {
		if options.ProxyURL == "" && options.ProxySocksURL == "" {
			if valid := assignProxy(proxy, options); valid {
				done <- true
			}
		}
	}
}
func testProxyConnection(proxy string) (string, error) {
	ip, _ := url.Parse(proxy)
	timeout := time.Duration(1 * time.Second)
	_, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%s", ip.Hostname(), ip.Port()), timeout)
	if err != nil {
		return "", err
	}
	return proxy, nil
}
func assignProxy(proxy string, options *types.Options) bool {
	var validConfig bool = true
	if strings.HasPrefix(proxy, "http") || strings.HasPrefix(proxy, "https") {
		options.ProxyURL = proxy
		options.ProxySocksURL = ""
	} else if strings.HasPrefix(proxy, "socks5") || strings.HasPrefix(proxy, "socks4") {
		options.ProxyURL = ""
		options.ProxySocksURL = proxy
	} else {
		validConfig = false
	}
	return validConfig

}
func validateTemplatePaths(templatesDirectory string, templatePaths, workflowPaths []string) {
	allGivenTemplatePaths := append(templatePaths, workflowPaths...)

	for _, templatePath := range allGivenTemplatePaths {
		if templatesDirectory != templatePath && filepath.IsAbs(templatePath) {
			fileInfo, err := os.Stat(templatePath)
			if err == nil && fileInfo.IsDir() {
				relativizedPath, err2 := filepath.Rel(templatesDirectory, templatePath)
				if err2 != nil || (len(relativizedPath) >= 2 && relativizedPath[:2] == "..") {
					gologger.Warning().Msgf("The given path (%s) is outside the default template directory path (%s)! "+
						"Referenced sub-templates with relative paths in workflows will be resolved against the default template directory.", templatePath, templatesDirectory)
					break
				}
			}
		}
	}
}
