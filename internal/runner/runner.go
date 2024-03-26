package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/projectdiscovery/nuclei/v3/internal/pdcp"
	"github.com/projectdiscovery/nuclei/v3/pkg/authprovider"
	"github.com/projectdiscovery/nuclei/v3/pkg/cruisecontrol"
	"github.com/projectdiscovery/nuclei/v3/pkg/input/provider"
	"github.com/projectdiscovery/nuclei/v3/pkg/installer"
	"github.com/projectdiscovery/nuclei/v3/pkg/loader/parser"
	uncoverlib "github.com/projectdiscovery/uncover"
	pdcpauth "github.com/projectdiscovery/utils/auth/pdcp"
	"github.com/projectdiscovery/utils/env"
	fileutil "github.com/projectdiscovery/utils/file"
	permissionutil "github.com/projectdiscovery/utils/permission"
	updateutils "github.com/projectdiscovery/utils/update"

	"github.com/logrusorgru/aurora"
	"github.com/pkg/errors"

	"github.com/projectdiscovery/gologger"
	"github.com/projectdiscovery/nuclei/v3/internal/colorizer"
	"github.com/projectdiscovery/nuclei/v3/pkg/catalog"
	"github.com/projectdiscovery/nuclei/v3/pkg/catalog/config"
	"github.com/projectdiscovery/nuclei/v3/pkg/catalog/disk"
	"github.com/projectdiscovery/nuclei/v3/pkg/catalog/loader"
	"github.com/projectdiscovery/nuclei/v3/pkg/core"
	"github.com/projectdiscovery/nuclei/v3/pkg/external/customtemplates"
	"github.com/projectdiscovery/nuclei/v3/pkg/input"
	parsers "github.com/projectdiscovery/nuclei/v3/pkg/loader/workflow"
	"github.com/projectdiscovery/nuclei/v3/pkg/output"
	"github.com/projectdiscovery/nuclei/v3/pkg/progress"
	"github.com/projectdiscovery/nuclei/v3/pkg/projectfile"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/automaticscan"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/contextargs"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/hosterrorscache"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/interactsh"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/protocolinit"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/uncover"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/utils/excludematchers"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/headless/engine"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/http/httpclientpool"
	"github.com/projectdiscovery/nuclei/v3/pkg/reporting"
	"github.com/projectdiscovery/nuclei/v3/pkg/templates"
	"github.com/projectdiscovery/nuclei/v3/pkg/types"
	"github.com/projectdiscovery/nuclei/v3/pkg/utils"
	"github.com/projectdiscovery/nuclei/v3/pkg/utils/stats"
	"github.com/projectdiscovery/nuclei/v3/pkg/utils/yaml"
	"github.com/projectdiscovery/retryablehttp-go"
	ptrutil "github.com/projectdiscovery/utils/ptr"
)

var (
	// HideAutoSaveMsg is a global variable to hide the auto-save message
	HideAutoSaveMsg = false
	// EnableCloudUpload is global variable to enable cloud upload
	EnableCloudUpload = false
)

func init() {
	HideAutoSaveMsg = env.GetEnvOrDefault("DISABLE_CLOUD_UPLOAD_WRN", false)
	EnableCloudUpload = env.GetEnvOrDefault("ENABLE_CLOUD_UPLOAD", false)
}

// Runner is a client for running the enumeration process.
type Runner struct {
	output           output.Writer
	interactsh       *interactsh.Client
	options          *types.Options
	projectFile      *projectfile.ProjectFile
	catalog          catalog.Catalog
	progress         progress.Progress
	colorizer        aurora.Aurora
	issuesClient     reporting.Client
	browser          *engine.Browser
	hostErrors       hosterrorscache.CacheInterface
	resumeCfg        *types.ResumeCfg
	pprofServer      *http.Server
	pdcpUploadErrMsg string
	inputProvider    provider.InputProvider
	//general purpose temporary directory
	tmpDir        string
	parser        parser.Parser
	cruiseControl *cruisecontrol.CruiseControl
}

const pprofServerAddress = "127.0.0.1:8086"

// New creates a new client for running the enumeration process.
func New(options *types.Options) (*Runner, error) {
	runner := &Runner{
		options: options,
	}

	if options.HealthCheck {
		gologger.Print().Msgf("%s\n", DoHealthCheck(options))
		os.Exit(0)
	}

	//  Version check by default
	if config.DefaultConfig.CanCheckForUpdates() {
		if err := installer.NucleiVersionCheck(); err != nil {
			if options.Verbose || options.Debug {
				gologger.Error().Msgf("nuclei version check failed got: %s\n", err)
			}
		}

		// check for custom template updates and update if available
		ctm, err := customtemplates.NewCustomTemplatesManager(options)
		if err != nil {
			gologger.Error().Label("custom-templates").Msgf("Failed to create custom templates manager: %s\n", err)
		}

		// Check for template updates and update if available.
		// If the custom templates manager is not nil, we will install custom templates if there is a fresh installation
		tm := &installer.TemplateManager{
			CustomTemplates:        ctm,
			DisablePublicTemplates: options.PublicTemplateDisableDownload,
		}
		if err := tm.FreshInstallIfNotExists(); err != nil {
			gologger.Warning().Msgf("failed to install nuclei templates: %s\n", err)
		}
		if err := tm.UpdateIfOutdated(); err != nil {
			gologger.Warning().Msgf("failed to update nuclei templates: %s\n", err)
		}

		if config.DefaultConfig.NeedsIgnoreFileUpdate() {
			if err := installer.UpdateIgnoreFile(); err != nil {
				gologger.Warning().Msgf("failed to update nuclei ignore file: %s\n", err)
			}
		}

		if options.UpdateTemplates {
			// we automatically check for updates unless explicitly disabled
			// this print statement is only to inform the user that there are no updates
			if !config.DefaultConfig.NeedsTemplateUpdate() {
				gologger.Info().Msgf("No new updates found for nuclei templates")
			}
			// manually trigger update of custom templates
			if ctm != nil {
				ctm.Update(context.TODO())
			}
		}
	}

	parser := templates.NewParser()

	if options.Validate {
		parser.ShouldValidate = true
	}
	// TODO: refactor to pass options reference globally without cycles
	parser.NoStrictSyntax = options.NoStrictSyntax
	runner.parser = parser

	yaml.StrictSyntax = !options.NoStrictSyntax

	if options.Headless {
		if engine.MustDisableSandbox() {
			gologger.Warning().Msgf("The current platform and privileged user will run the browser without sandbox\n")
		}
		browser, err := engine.New(options)
		if err != nil {
			return nil, err
		}
		runner.browser = browser
	}

	runner.catalog = disk.NewCatalog(config.DefaultConfig.TemplatesDirectory)

	var httpclient *retryablehttp.Client
	if options.ProxyInternal && types.ProxyURL != "" || types.ProxySocksURL != "" {
		var err error
		httpclient, err = httpclientpool.Get(options, &httpclientpool.Configuration{})
		if err != nil {
			return nil, err
		}
	}

	if err := reporting.CreateConfigIfNotExists(); err != nil {
		return nil, err
	}
	reportingOptions, err := createReportingOptions(options)
	if err != nil {
		return nil, err
	}
	if reportingOptions != nil && httpclient != nil {
		reportingOptions.HttpClient = httpclient
	}

	if reportingOptions != nil {
		client, err := reporting.New(reportingOptions, options.ReportingDB, false)
		if err != nil {
			return nil, errors.Wrap(err, "could not create issue reporting client")
		}
		runner.issuesClient = client
	}

	// output coloring
	useColor := !options.NoColor
	runner.colorizer = aurora.NewAurora(useColor)
	templates.Colorizer = runner.colorizer
	templates.SeverityColorizer = colorizer.New(runner.colorizer)

	if options.EnablePprof {
		server := &http.Server{
			Addr:    pprofServerAddress,
			Handler: http.DefaultServeMux,
		}
		gologger.Info().Msgf("Listening pprof debug server on: %s", pprofServerAddress)
		runner.pprofServer = server
		go func() {
			_ = server.ListenAndServe()
		}()
	}

	if (len(options.Templates) == 0 || !options.NewTemplates || (options.TargetsFilePath == "" && !options.Stdin && len(options.Targets) == 0)) && options.UpdateTemplates {
		os.Exit(0)
	}

	// create the input provider and load the inputs
	inputProvider, err := provider.NewInputProvider(provider.InputOptions{Options: options})
	if err != nil {
		return nil, errors.Wrap(err, "could not create input provider")
	}
	runner.inputProvider = inputProvider

	// Create the output file if asked
	outputWriter, err := output.NewStandardWriter(options)
	if err != nil {
		return nil, errors.Wrap(err, "could not create output file")
	}
	// setup a proxy writer to automatically upload results to PDCP
	runner.output = runner.setupPDCPUpload(outputWriter)

	if options.JSONL && options.EnableProgressBar {
		options.StatsJSON = true
	}
	if options.StatsJSON {
		options.EnableProgressBar = true
	}
	// Creates the progress tracking object
	var progressErr error
	statsInterval := options.StatsInterval
	runner.progress, progressErr = progress.NewStatsTicker(statsInterval, options.EnableProgressBar, options.StatsJSON, false, options.MetricsPort)
	if progressErr != nil {
		return nil, progressErr
	}

	// create project file if requested or load the existing one
	if options.Project {
		var projectFileErr error
		runner.projectFile, projectFileErr = projectfile.New(&projectfile.Options{Path: options.ProjectPath, Cleanup: utils.IsBlank(options.ProjectPath)})
		if projectFileErr != nil {
			return nil, projectFileErr
		}
	}

	// create the resume configuration structure
	resumeCfg := types.NewResumeCfg()
	if runner.options.ShouldLoadResume() {
		gologger.Info().Msg("Resuming from save checkpoint")
		file, err := os.ReadFile(runner.options.Resume)
		if err != nil {
			return nil, err
		}
		err = json.Unmarshal(file, &resumeCfg)
		if err != nil {
			return nil, err
		}
		resumeCfg.Compile()
	}
	runner.resumeCfg = resumeCfg

	opts := interactsh.DefaultOptions(runner.output, runner.issuesClient, runner.progress)
	opts.Debug = runner.options.Debug
	opts.NoColor = runner.options.NoColor
	if options.InteractshURL != "" {
		opts.ServerURL = options.InteractshURL
	}
	opts.Authorization = options.InteractshToken
	opts.CacheSize = options.InteractionsCacheSize
	opts.Eviction = time.Duration(options.InteractionsEviction) * time.Second
	opts.CooldownPeriod = time.Duration(options.InteractionsCoolDownPeriod) * time.Second
	opts.PollDuration = time.Duration(options.InteractionsPollDuration) * time.Second
	opts.NoInteractsh = runner.options.NoInteractsh
	opts.StopAtFirstMatch = runner.options.StopAtFirstMatch
	opts.Debug = runner.options.Debug
	opts.DebugRequest = runner.options.DebugRequests
	opts.DebugResponse = runner.options.DebugResponse

	if err := options.ParseCruiseControl(); err != nil {
		return nil, err
	}

	runner.cruiseControl, err = cruisecontrol.New(cruisecontrol.ParseOptionsFrom(options))
	if err != nil {
		return nil, err
	}

	if httpclient != nil {
		opts.HTTPClient = httpclient
	}
	if opts.HTTPClient == nil {
		httpOpts := retryablehttp.DefaultOptionsSingle
		httpOpts.Timeout = runner.cruiseControl.Standard().Durations.Timeout
		// in testing it was found most of times when interactsh failed, it was due to failure in registering /polling requests
		opts.HTTPClient = retryablehttp.NewClient(retryablehttp.DefaultOptionsSingle)
	}
	interactshClient, err := interactsh.New(opts)
	if err != nil {
		gologger.Error().Msgf("Could not create interactsh client: %s", err)
	} else {
		runner.interactsh = interactshClient
	}

	if tmpDir, err := os.MkdirTemp("", "nuclei-tmp-*"); err == nil {
		runner.tmpDir = tmpDir
	}

	return runner, nil
}

// runStandardEnumeration runs standard enumeration
func (r *Runner) runStandardEnumeration(executerOpts protocols.ExecutorOptions, store *loader.Store, engine *core.Engine) (*atomic.Bool, error) {
	if r.options.AutomaticScan {
		return r.executeSmartWorkflowInput(executerOpts, store, engine)
	}
	return r.executeTemplatesInput(store, engine)
}

// Close releases all the resources and cleans up
func (r *Runner) Close() {
	if r.output != nil {
		r.output.Close()
	}
	if r.issuesClient != nil {
		r.issuesClient.Close()
	}
	if r.projectFile != nil {
		r.projectFile.Close()
	}
	if r.inputProvider != nil {
		r.inputProvider.Close()
	}
	protocolinit.Close()
	if r.pprofServer != nil {
		_ = r.pprofServer.Shutdown(context.Background())
	}
	if r.cruiseControl != nil {
		r.cruiseControl.Close()
	}
	r.progress.Stop()
	if r.browser != nil {
		r.browser.Close()
	}
	if r.tmpDir != "" {
		_ = os.RemoveAll(r.tmpDir)
	}
}

// setupPDCPUpload sets up the PDCP upload writer
// by creating a new writer and returning it
func (r *Runner) setupPDCPUpload(writer output.Writer) output.Writer {
	// if scanid is given implicitly consider that scan upload is enabled
	if r.options.ScanID != "" {
		r.options.EnableCloudUpload = true
	}
	if !(r.options.EnableCloudUpload || EnableCloudUpload) {
		r.pdcpUploadErrMsg = fmt.Sprintf("[%v] Scan results upload to cloud is disabled.", aurora.BrightYellow("WRN"))
		return writer
	}
	color := aurora.NewAurora(!r.options.NoColor)
	h := &pdcpauth.PDCPCredHandler{}
	creds, err := h.GetCreds()
	if err != nil {
		if err != pdcpauth.ErrNoCreds && !HideAutoSaveMsg {
			gologger.Verbose().Msgf("Could not get credentials for cloud upload: %s\n", err)
		}
		r.pdcpUploadErrMsg = fmt.Sprintf("[%v] To view results on Cloud Dashboard, Configure API key from %v", color.BrightYellow("WRN"), pdcpauth.DashBoardURL)
		return writer
	}
	uploadWriter, err := pdcp.NewUploadWriter(context.Background(), creds)
	if err != nil {
		r.pdcpUploadErrMsg = fmt.Sprintf("[%v] PDCP (%v) Auto-Save Failed: %s\n", color.BrightYellow("WRN"), pdcpauth.DashBoardURL, err)
		return writer
	}
	if r.options.ScanID != "" {
		uploadWriter.SetScanID(r.options.ScanID)
	}
	return output.NewMultiWriter(writer, uploadWriter)
}

// RunEnumeration sets up the input layer for giving input nuclei.
// binary and runs the actual enumeration
func (r *Runner) RunEnumeration() error {
	// If user asked for new templates to be executed, collect the list from the templates' directory.
	if r.options.NewTemplates {
		if arr := config.DefaultConfig.GetNewAdditions(); len(arr) > 0 {
			r.options.Templates = append(r.options.Templates, arr...)
		}
	}
	if len(r.options.NewTemplatesWithVersion) > 0 {
		if arr := installer.GetNewTemplatesInVersions(r.options.NewTemplatesWithVersion...); len(arr) > 0 {
			r.options.Templates = append(r.options.Templates, arr...)
		}
	}
	// Exclude ignored file for validation
	if !r.options.Validate {
		ignoreFile := config.ReadIgnoreFile()
		r.options.ExcludeTags = append(r.options.ExcludeTags, ignoreFile.Tags...)
		r.options.ExcludedTemplates = append(r.options.ExcludedTemplates, ignoreFile.Files...)
	}

	// Create the executor options which will be used throughout the execution
	// stage by the nuclei engine modules.
	executorOpts := protocols.ExecutorOptions{
		Output:             r.output,
		Options:            r.options,
		Progress:           r.progress,
		Catalog:            r.catalog,
		IssuesClient:       r.issuesClient,
		CruiseControl:      r.cruiseControl,
		Interactsh:         r.interactsh,
		ProjectFile:        r.projectFile,
		Browser:            r.browser,
		Colorizer:          r.colorizer,
		ResumeCfg:          r.resumeCfg,
		ExcludeMatchers:    excludematchers.New(r.options.ExcludeMatchers),
		InputHelper:        input.NewHelper(),
		TemporaryDirectory: r.tmpDir,
		Parser:             r.parser,
	}

	if len(r.options.SecretsFile) > 0 && !r.options.Validate {
		authTmplStore, err := GetAuthTmplStore(*r.options, r.catalog, executorOpts)
		if err != nil {
			return errors.Wrap(err, "failed to load dynamic auth templates")
		}
		authOpts := &authprovider.AuthProviderOptions{SecretsFiles: r.options.SecretsFile}
		authOpts.LazyFetchSecret = GetLazyAuthFetchCallback(&AuthLazyFetchOptions{
			TemplateStore: authTmplStore,
			ExecOpts:      executorOpts,
		})
		// initialize auth provider
		provider, err := authprovider.NewAuthProvider(authOpts)
		if err != nil {
			return errors.Wrap(err, "could not create auth provider")
		}
		executorOpts.AuthProvider = provider
	}

	if r.options.ShouldUseHostError() {
		cache := hosterrorscache.New(r.options.MaxHostError, hosterrorscache.DefaultMaxHostsCount, r.options.TrackError)
		cache.SetVerbose(r.options.Verbose)
		r.hostErrors = cache
		executorOpts.HostErrorsCache = cache
	}

	executorEngine := core.New(r.options)
	executorEngine.SetExecuterOptions(executorOpts)

	workflowLoader, err := parsers.NewLoader(&executorOpts)
	if err != nil {
		return errors.Wrap(err, "Could not create loader.")
	}
	executorOpts.WorkflowLoader = workflowLoader

	// If using input-file flags, only load http fuzzing based templates.
	loaderConfig := loader.NewConfig(r.options, r.catalog, executorOpts)
	if !strings.EqualFold(r.options.InputFileMode, "list") || r.options.FuzzTemplates {
		// if input type is not list (implicitly enable fuzzing)
		r.options.FuzzTemplates = true
		loaderConfig.OnlyLoadHTTPFuzzing = true
	}
	store, err := loader.New(loaderConfig)
	if err != nil {
		return errors.Wrap(err, "Could not create loader.")
	}

	if r.options.Validate {
		if err := store.ValidateTemplates(); err != nil {
			return err
		}
		if stats.GetValue(templates.SyntaxErrorStats) == 0 && stats.GetValue(templates.SyntaxWarningStats) == 0 && stats.GetValue(templates.RuntimeWarningsStats) == 0 {
			gologger.Info().Msgf("All templates validated successfully\n")
		} else {
			return errors.New("encountered errors while performing template validation")
		}
		return nil // exit
	}
	store.Load()
	// TODO: remove below functions after v3 or update warning messages
	disk.PrintDeprecatedPathsMsgIfApplicable(r.options.Silent)
	templates.PrintDeprecatedProtocolNameMsgIfApplicable(r.options.Silent, r.options.Verbose)

	// add the hosts from the metadata queries of loaded templates into input provider
	if r.options.Uncover && len(r.options.UncoverQuery) == 0 {
		uncoverOpts := &uncoverlib.Options{
			Limit:    r.options.UncoverLimit,
			MaxRetry: r.options.Retries,
			// todo: timeout should be time.Duration
			Timeout:       int(r.cruiseControl.Standard().Durations.Timeout.Seconds()),
			RateLimit:     uint(r.options.UncoverRateLimit),
			RateLimitUnit: time.Minute, // default unit is minute
		}
		ret := uncover.GetUncoverTargetsFromMetadata(context.TODO(), store.Templates(), r.options.UncoverField, uncoverOpts)
		for host := range ret {
			_ = r.inputProvider.SetWithExclusions(host)
		}
	}
	// list all templates
	if r.options.TemplateList || r.options.TemplateDisplay {
		r.listAvailableStoreTemplates(store)
		os.Exit(0)
	}

	// display execution info like version , templates used etc
	r.displayExecutionInfo(store)

	// prefetch secrets if enabled
	if executorOpts.AuthProvider != nil && r.options.PreFetchSecrets {
		gologger.Info().Msgf("Pre-fetching secrets from authprovider[s]")
		if err := executorOpts.AuthProvider.PreFetchSecrets(); err != nil {
			return errors.Wrap(err, "could not pre-fetch secrets")
		}
	}

	// If not explicitly disabled, check if http based protocols
	// are used, and if inputs are non-http to pre-perform probing
	// of urls and storing them for execution.
	if !r.options.DisableHTTPProbe && loader.IsHTTPBasedProtocolUsed(store) && r.isInputNonHTTP() {
		inputHelpers, err := r.initializeTemplatesHTTPInput()
		if err != nil {
			return errors.Wrap(err, "could not probe http input")
		}
		executorOpts.InputHelper.InputsHTTP = inputHelpers
	}

	enumeration := false
	var results *atomic.Bool
	results, err = r.runStandardEnumeration(executorOpts, store, executorEngine)
	enumeration = true

	if !enumeration {
		return err
	}

	if r.interactsh != nil {
		matched := r.interactsh.Close()
		if matched {
			results.CompareAndSwap(false, true)
		}
	}
	if executorOpts.InputHelper != nil {
		_ = executorOpts.InputHelper.Close()
	}

	// todo: error propagation without canonical straight error check is required by cloud?
	// use safe dereferencing to avoid potential panics in case of previous unchecked errors
	if v := ptrutil.Safe(results); !v.Load() {
		gologger.Info().Msgf("No results found. Better luck next time!")
	}
	// check if a passive scan was requested but no target was provided
	if r.options.OfflineHTTP && len(r.options.Targets) == 0 && r.options.TargetsFilePath == "" {
		return errors.Wrap(err, "missing required input (http response) to run passive templates")
	}

	return err
}

func (r *Runner) isInputNonHTTP() bool {
	var nonURLInput bool
	r.inputProvider.Iterate(func(value *contextargs.MetaInput) bool {
		if !strings.Contains(value.Input, "://") {
			nonURLInput = true
			return false
		}
		return true
	})
	return nonURLInput
}

func (r *Runner) executeSmartWorkflowInput(executorOpts protocols.ExecutorOptions, store *loader.Store, engine *core.Engine) (*atomic.Bool, error) {
	r.progress.Init(r.inputProvider.Count(), 0, 0)

	service, err := automaticscan.New(automaticscan.Options{
		ExecuterOpts: executorOpts,
		Store:        store,
		Engine:       engine,
		Target:       r.inputProvider,
	})
	if err != nil {
		return nil, errors.Wrap(err, "could not create automatic scan service")
	}
	if err := service.Execute(); err != nil {
		return nil, errors.Wrap(err, "could not execute automatic scan")
	}
	result := &atomic.Bool{}
	result.Store(service.Close())
	return result, nil
}

func (r *Runner) executeTemplatesInput(store *loader.Store, engine *core.Engine) (*atomic.Bool, error) {
	if r.options.VerboseVerbose {
		for _, template := range store.Templates() {
			r.logAvailableTemplate(template.Path)
		}
		for _, template := range store.Workflows() {
			r.logAvailableTemplate(template.Path)
		}
	}

	finalTemplates := []*templates.Template{}
	finalTemplates = append(finalTemplates, store.Templates()...)
	finalTemplates = append(finalTemplates, store.Workflows()...)

	if len(finalTemplates) == 0 {
		return nil, errors.New("no templates provided for scan")
	}

	// pass input provider to engine
	// TODO: this should be not necessary after r.hmapInputProvider is removed + refactored
	if r.inputProvider == nil {
		return nil, errors.New("no input provider found")
	}
	results := engine.ExecuteScanWithOpts(finalTemplates, r.inputProvider, r.options.DisableClustering)
	return results, nil
}

// displayExecutionInfo displays misc info about the nuclei engine execution
func (r *Runner) displayExecutionInfo(store *loader.Store) {
	// Display stats for any loaded templates' syntax warnings or errors
	stats.Display(templates.SyntaxWarningStats)
	stats.Display(templates.SyntaxErrorStats)
	stats.Display(templates.RuntimeWarningsStats)
	if r.options.Verbose {
		// only print these stats in verbose mode
		stats.DisplayAsWarning(templates.HeadlessFlagWarningStats)
		stats.DisplayAsWarning(templates.CodeFlagWarningStats)
		stats.DisplayAsWarning(templates.TemplatesExecutedStats)
		stats.DisplayAsWarning(templates.HeadlessFlagWarningStats)
		stats.DisplayAsWarning(templates.CodeFlagWarningStats)
		stats.DisplayAsWarning(templates.FuzzFlagWarningStats)
		stats.DisplayAsWarning(templates.TemplatesExecutedStats)
	}

	stats.DisplayAsWarning(templates.UnsignedCodeWarning)
	stats.ForceDisplayWarning(templates.SkippedUnsignedStats)

	cfg := config.DefaultConfig

	gologger.Info().Msgf("Current nuclei version: %v %v", config.Version, updateutils.GetVersionDescription(config.Version, cfg.LatestNucleiVersion))
	gologger.Info().Msgf("Current nuclei-templates version: %v %v", cfg.TemplateVersion, updateutils.GetVersionDescription(cfg.TemplateVersion, cfg.LatestNucleiTemplatesVersion))
	if !HideAutoSaveMsg {
		if r.pdcpUploadErrMsg != "" {
			gologger.Print().Msgf("%s", r.pdcpUploadErrMsg)
		} else {
			gologger.Info().Msgf("To view results on cloud dashboard, visit %v/scans upon scan completion.", pdcpauth.DashBoardURL)
		}
	}

	if len(store.Templates()) > 0 {
		gologger.Info().Msgf("New templates added in latest release: %d", len(config.DefaultConfig.GetNewAdditions()))
		gologger.Info().Msgf("Templates loaded for current scan: %d", len(store.Templates()))
	}
	if len(store.Workflows()) > 0 {
		gologger.Info().Msgf("Workflows loaded for current scan: %d", len(store.Workflows()))
	}
	for k, v := range templates.SignatureStats {
		value := v.Load()
		if k == templates.Unsigned && value > 0 {
			// adjust skipped unsigned templates via code or -dut flag
			value = value - uint64(stats.GetValue(templates.SkippedUnsignedStats))
			value = value - uint64(stats.GetValue(templates.CodeFlagWarningStats))
		}
		if value > 0 {
			if k != templates.Unsigned {
				gologger.Info().Msgf("Executing %d signed templates from %s", value, k)
			} else if !r.options.Silent && !config.DefaultConfig.HideTemplateSigWarning {
				gologger.Print().Msgf("[%v] Loaded %d unsigned templates for scan. Use with caution.", aurora.BrightYellow("WRN"), value)
			}
		}
	}

	if r.inputProvider.Count() > 0 {
		gologger.Info().Msgf("Targets loaded for current scan: %d", r.inputProvider.Count())
	}
}

// SaveResumeConfig to file
func (r *Runner) SaveResumeConfig(path string) error {
	dir := filepath.Dir(path)
	if !fileutil.FolderExists(dir) {
		if err := os.MkdirAll(dir, os.ModePerm); err != nil {
			return err
		}
	}
	resumeCfgClone := r.resumeCfg.Clone()
	resumeCfgClone.ResumeFrom = resumeCfgClone.Current
	data, _ := json.MarshalIndent(resumeCfgClone, "", "\t")

	return os.WriteFile(path, data, permissionutil.ConfigFilePermission)
}
