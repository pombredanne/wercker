package main

import (
	"fmt"

	"golang.org/x/net/context"
)

// Pipeline is a set of steps to run, this is the interface shared by
// both Build and Deploy
type Pipeline interface {
	// Getters
	Env() *Environment       // base
	Box() *Box               // base
	Services() []*ServiceBox //base
	Steps() []IStep          // base
	AfterSteps() []IStep     // base

	// Methods
	CommonEnv() [][]string // base
	InitEnv(*Environment)  // impl
	CollectArtifact(string) (*Artifact, error)
	SetupGuest(context.Context, *Session) error
	ExportEnvironment(context.Context, *Session) error

	LogEnvironment()
	DockerRepo() string
	DockerTag() string
	DockerMessage() string
}

// PipelineResult keeps track of the results of a build or deploy
// mostly so that we can use it to run after-steps
type PipelineResult struct {
	Success           bool
	FailedStepName    string
	FailedStepMessage string
}

// ExportEnvironment for this pipeline result (used in after-steps)
func (pr *PipelineResult) ExportEnvironment(sessionCtx context.Context, sess *Session) error {
	e := &Environment{}
	result := "failed"
	if pr.Success {
		result = "passed"
	}
	e.Add("WERCKER_RESULT", result)
	if !pr.Success {
		e.Add("WERCKER_FAILED_STEP_DISPLAY_NAME", pr.FailedStepName)
		e.Add("WERCKER_FAILED_STEP_MESSAGE", pr.FailedStepMessage)
	}

	exit, _, err := sess.SendChecked(sessionCtx, e.Export()...)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("Pipeline failed with exit code: %d", exit)
	}
	return nil
}

// BasePipeline is the base class for Build and Deploy
type BasePipeline struct {
	options    *PipelineOptions
	config     *PipelineConfig
	env        *Environment
	box        *Box
	services   []*ServiceBox
	steps      []IStep
	afterSteps []IStep
	logger     *LogEntry
}

// NewBasePipeline initialize our pipeline from our configs
func NewBasePipeline(options *PipelineOptions, pipelineConfig *RawPipelineConfig, boxConfig *RawBoxConfig, servicesConfig []*RawBoxConfig, stepsConfig RawStepsConfig, afterStepsConfig RawStepsConfig) (*BasePipeline, error) {

	box, err := boxConfig.ToBox(options, &BoxOptions{})
	if err != nil {
		return nil, err
	}

	var services []*ServiceBox
	for _, sbox := range servicesConfig {
		service, err := sbox.ToServiceBox(options, &BoxOptions{})
		if err != nil {
			return nil, err
		}
		services = append(services, service)
	}

	initStep, err := NewWerckerInitStep(options)
	if err != nil {
		return nil, err
	}

	steps := []IStep{initStep}
	realSteps, err := stepsConfig.ToSteps(options)
	if err != nil {
		return nil, err
	}
	steps = append(steps, realSteps...)

	var afterSteps []IStep
	if afterStepsConfig != nil {
		afterSteps = []IStep{initStep}
		realAfterSteps, err := afterStepsConfig.ToSteps(options)
		if err != nil {
			return nil, err
		}
		afterSteps = append(afterSteps, realAfterSteps...)
	}

	logger := rootLogger.WithField("Logger", "Pipeline")
	return &BasePipeline{
		options:    options,
		env:        &Environment{},
		box:        box,
		services:   services,
		steps:      steps,
		afterSteps: afterSteps,
		logger:     logger,
	}, nil
}

// Box is a getter for the box
func (p *BasePipeline) Box() *Box {
	return p.box
}

// Services is a getter for the Services
func (p *BasePipeline) Services() []*ServiceBox {
	return p.services
}

// Steps is a getter for steps
func (p *BasePipeline) Steps() []IStep {
	return p.steps
}

// AfterSteps is a getter for afterSteps
func (p *BasePipeline) AfterSteps() []IStep {
	return p.afterSteps
}

// Env is a getter for env
func (p *BasePipeline) Env() *Environment {
	return p.env
}

// CommonEnv is shared by both builds and deploys
func (p *BasePipeline) CommonEnv() [][]string {
	a := [][]string{
		[]string{"WERCKER", "true"},
		[]string{"WERCKER_ROOT", p.options.GuestPath("source")},
		[]string{"WERCKER_SOURCE_DIR", p.options.GuestPath("source", p.options.SourceDir)},
		// TODO(termie): Support cache dir
		[]string{"WERCKER_CACHE_DIR", "/cache"},
		[]string{"WERCKER_OUTPUT_DIR", p.options.GuestPath("output")},
		[]string{"WERCKER_PIPELINE_DIR", p.options.GuestPath()},
		[]string{"WERCKER_REPORT_DIR", p.options.GuestPath("report")},
		[]string{"WERCKER_APPLICATION_ID", p.options.ApplicationID},
		[]string{"WERCKER_APPLICATION_NAME", p.options.ApplicationName},
		[]string{"WERCKER_APPLICATION_OWNER_NAME", p.options.ApplicationOwnerName},
		[]string{"WERCKER_APPLICATION_URL", fmt.Sprintf("%s/#application/%s", p.options.BaseURL, p.options.ApplicationID)},
		//[]string{"WERCKER_STARTED_BY", ...},
		[]string{"TERM", "xterm-256color"},
	}
	return a
}

// SetupGuest ensures that the guest is prepared to run the pipeline.
func (p *BasePipeline) SetupGuest(sessionCtx context.Context, sess *Session) error {
	sess.HideLogs()
	defer sess.ShowLogs()

	cmds := []string{}

	// If we're running in direct-mount mode we mounted stuff read-write and
	// won't need to copy
	if !p.options.DirectMount {
		cmds = append(cmds, []string{
			// Make sure our guest path exists
			fmt.Sprintf(`mkdir "%s"`, p.options.GuestPath()),
			// Make sure the output path exists
			// Copy the source from the mounted directory to the pipeline dir
			fmt.Sprintf(`cp -r "%s" "%s"`, p.options.MntPath("source"), p.options.GuestPath("source")),
		}...)
	}

	cmds = append(cmds, []string{
		fmt.Sprintf(`mkdir "%s"`, p.options.GuestPath("output")),
		// Make sure the cachedir exists
		fmt.Sprintf(`mkdir "%s"`, "/cache"),
	}...)

	for _, cmd := range cmds {
		exit, _, err := sess.SendChecked(sessionCtx, cmd)
		if err != nil {
			return err
		}
		if exit != 0 {
			return fmt.Errorf("Guest command failed: %s", cmd)
		}
	}

	return nil
}

// ExportEnvironment to the session
func (p *BasePipeline) ExportEnvironment(sessionCtx context.Context, sess *Session) error {
	exit, _, err := sess.SendChecked(sessionCtx, p.env.Export()...)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("Build failed with exit code: %d", exit)
	}
	return nil
}

// LogEnvironment dumps the base environment
func (p *BasePipeline) LogEnvironment() {
	p.logger.Debugln("Base Pipeline Environment:")
	for _, pair := range p.env.Ordered() {
		p.logger.Debugln(" ", pair[0], pair[1])
	}
}
