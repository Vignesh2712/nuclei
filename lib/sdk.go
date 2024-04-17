package nuclei

import (
	"bufio"
	"bytes"
	"io"

	"github.com/projectdiscovery/nuclei/v3/pkg/authprovider"
	"github.com/projectdiscovery/nuclei/v3/pkg/catalog/disk"
	"github.com/projectdiscovery/nuclei/v3/pkg/catalog/loader"
	"github.com/projectdiscovery/nuclei/v3/pkg/core"
	"github.com/projectdiscovery/nuclei/v3/pkg/input/provider"
	providerTypes "github.com/projectdiscovery/nuclei/v3/pkg/input/types"
	"github.com/projectdiscovery/nuclei/v3/pkg/loader/workflow"
	"github.com/projectdiscovery/nuclei/v3/pkg/output"
	"github.com/projectdiscovery/nuclei/v3/pkg/progress"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/hosterrorscache"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/common/interactsh"
	"github.com/projectdiscovery/nuclei/v3/pkg/protocols/headless/engine"
	"github.com/projectdiscovery/nuclei/v3/pkg/reporting"
	"github.com/projectdiscovery/nuclei/v3/pkg/templates"
	"github.com/projectdiscovery/nuclei/v3/pkg/templates/signer"
	"github.com/projectdiscovery/nuclei/v3/pkg/types"
	"github.com/projectdiscovery/ratelimit"
	"github.com/projectdiscovery/retryablehttp-go"
	errorutil "github.com/projectdiscovery/utils/errors"
)

// NucleiSDKOptions contains options for nuclei SDK
type NucleiSDKOptions func(e *NucleiEngine) error

var (
	// ErrNotImplemented is returned when a feature is not implemented
	ErrNotImplemented = errorutil.New("Not implemented")
	// ErrNoTemplatesAvailable is returned when no templates are available to execute
	ErrNoTemplatesAvailable = errorutil.New("No templates available")
	// ErrNoTargetsAvailable is returned when no targets are available to scan
	ErrNoTargetsAvailable = errorutil.New("No targets available")
	// ErrOptionsNotSupported is returned when an option is not supported in thread safe mode
	ErrOptionsNotSupported = errorutil.NewWithFmt("Option %v not supported in thread safe mode")
)

type engineMode uint

const (
	singleInstance engineMode = iota
	threadSafe
)

// NucleiEngine is the Engine/Client for nuclei which
// runs scans using templates and returns results
type NucleiEngine struct {
	// user options
	resultCallbacks             []func(event *output.ResultEvent)
	onFailureCallback           func(event *output.InternalEvent)
	disableTemplatesAutoUpgrade bool
	enableStats                 bool
	onUpdateAvailableCallback   func(newVersion string)

	// ready-status fields
	templatesLoaded bool

	// unexported core fields
	interactshClient *interactsh.Client
	catalog          *disk.DiskCatalog
	rateLimiter      *ratelimit.Limiter
	store            *loader.Store
	httpxClient      providerTypes.InputLivenessProbe
	inputProvider    provider.InputProvider
	engine           *core.Engine
	mode             engineMode
	browserInstance  *engine.Browser
	httpClient       *retryablehttp.Client
	parser           *templates.Parser
	authprovider     authprovider.AuthProvider

	// unexported meta options
	opts           *types.Options
	interactshOpts *interactsh.Options
	hostErrCache   *hosterrorscache.Cache
	customWriter   output.Writer
	customProgress progress.Progress
	rc             reporting.Client
	executerOpts   protocols.ExecutorOptions
}

// LoadAllTemplates loads all nuclei template based on given options
func (e *NucleiEngine) LoadAllTemplates() error {
	workflowLoader, err := workflow.NewLoader(&e.executerOpts)
	if err != nil {
		return errorutil.New("Could not create workflow loader: %s\n", err)
	}
	e.executerOpts.WorkflowLoader = workflowLoader

	e.store, err = loader.New(loader.NewConfig(e.opts, e.catalog, e.executerOpts))
	if err != nil {
		return errorutil.New("Could not create loader client: %s\n", err)
	}
	e.store.Load()
	return nil
}

// GetTemplates returns all nuclei templates that are loaded
func (e *NucleiEngine) GetTemplates() []*templates.Template {
	if !e.templatesLoaded {
		_ = e.LoadAllTemplates()
	}
	return e.store.Templates()
}

// LoadTargets(urls/domains/ips only) adds targets to the nuclei engine
func (e *NucleiEngine) LoadTargets(targets []string, probeNonHttp bool) {
	for _, target := range targets {
		if probeNonHttp {
			_ = e.inputProvider.SetWithProbe(target, e.httpxClient)
		} else {
			e.inputProvider.Set(target)
		}
	}
}

// LoadTargetsFromReader adds targets(urls/domains/ips only) from reader to the nuclei engine
func (e *NucleiEngine) LoadTargetsFromReader(reader io.Reader, probeNonHttp bool) {
	buff := bufio.NewScanner(reader)
	for buff.Scan() {
		if probeNonHttp {
			_ = e.inputProvider.SetWithProbe(buff.Text(), e.httpxClient)
		} else {
			e.inputProvider.Set(buff.Text())
		}
	}
}

// LoadTargetsWithHttpData loads targets that contain http data from file it currently supports
// multiple formats like burp xml,openapi,swagger,proxify json
// Note: this is mutually exclusive with LoadTargets and LoadTargetsFromReader
func (e *NucleiEngine) LoadTargetsWithHttpData(filePath string, filemode string) error {
	e.opts.TargetsFilePath = filePath
	e.opts.InputFileMode = filemode
	httpProvider, err := provider.NewInputProvider(provider.InputOptions{Options: e.opts})
	if err != nil {
		e.opts.TargetsFilePath = ""
		e.opts.InputFileMode = ""
		return err
	}
	e.inputProvider = httpProvider
	return nil
}

// GetExecuterOptions returns the nuclei executor options
func (e *NucleiEngine) GetExecuterOptions() *protocols.ExecutorOptions {
	return &e.executerOpts
}

// ParseTemplate parses a template from given data
// template verification status can be accessed from template.Verified
func (e *NucleiEngine) ParseTemplate(data []byte) (*templates.Template, error) {
	return templates.ParseTemplateFromReader(bytes.NewReader(data), nil, e.executerOpts)
}

// SignTemplate signs the tempalate using given signer
func (e *NucleiEngine) SignTemplate(tmplSigner *signer.TemplateSigner, data []byte) ([]byte, error) {
	tmpl, err := e.ParseTemplate(data)
	if err != nil {
		return data, err
	}
	if tmpl.Verified {
		// already signed
		return data, nil
	}
	if len(tmpl.Workflows) > 0 {
		return data, templates.ErrNotATemplate
	}
	signatureData, err := tmplSigner.Sign(data, tmpl)
	if err != nil {
		return data, err
	}
	buff := bytes.NewBuffer(signer.RemoveSignatureFromData(data))
	buff.WriteString("\n" + signatureData)
	return buff.Bytes(), err
}

// Close all resources used by nuclei engine
func (e *NucleiEngine) Close() {
	e.interactshClient.Close()
	e.rc.Close()
	e.customWriter.Close()
	e.hostErrCache.Close()
	e.executerOpts.RateLimiter.Stop()
}

// ExecuteWithCallback executes templates on targets and calls callback on each result(only if results are found)
func (e *NucleiEngine) ExecuteWithCallback(callback ...func(event *output.ResultEvent)) error {
	if !e.templatesLoaded {
		_ = e.LoadAllTemplates()
	}
	if len(e.store.Templates()) == 0 && len(e.store.Workflows()) == 0 {
		return ErrNoTemplatesAvailable
	}
	if e.inputProvider.Count() == 0 {
		return ErrNoTargetsAvailable
	}

	filtered := []func(event *output.ResultEvent){}
	for _, callback := range callback {
		if callback != nil {
			filtered = append(filtered, callback)
		}
	}
	e.resultCallbacks = append(e.resultCallbacks, filtered...)

	_ = e.engine.ExecuteScanWithOpts(e.store.Templates(), e.inputProvider, false)
	defer e.engine.WorkPool().Wait()
	return nil
}

func (e *NucleiEngine) Options() *types.Options {
	return e.opts
}

func (e *NucleiEngine) Engine() *core.Engine {
	return e.engine
}

// NewNucleiEngine creates a new nuclei engine instance
func NewNucleiEngine(options ...NucleiSDKOptions) (*NucleiEngine, error) {
	// default options
	e := &NucleiEngine{
		opts: types.DefaultOptions(),
		mode: singleInstance,
	}
	for _, option := range options {
		if err := option(e); err != nil {
			return nil, err
		}
	}
	if err := e.init(); err != nil {
		return nil, err
	}
	return e, nil
}
