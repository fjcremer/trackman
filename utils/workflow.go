package utils

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
	"gopkg.in/yaml.v2"
)

// WorkflowOptions provides options for a workflow
type WorkflowOptions struct {
	Notifier    func(ctx context.Context, event *Event) error
	Concurrency int
	Timeout     time.Duration
}

// Workflow is the internal object to hold a workflow file
type Workflow struct {
	Version  string
	Metadata map[string]string
	Steps    []*Step

	options    *WorkflowOptions
	logger     *logrus.Logger
	gatekeeper *semaphore.Weighted
	signal     *sync.Mutex
	stopFlag   bool
}

// LoadWorkflowFromBytes loads a workflow from bytes
func LoadWorkflowFromBytes(ctx context.Context, options *WorkflowOptions, buff []byte) (*Workflow, error) {
	var workflow *Workflow
	err := yaml.Unmarshal(buff, &workflow)
	if err != nil {
		return nil, err
	}
	if options == nil {
		panic("no options")
	}
	if options.Notifier == nil {
		panic("no notifier")
	}

	workflow.logger, _ = LoggerContext(ctx)

	if workflow.Version != "1" {
		workflow.logger.Fatal("invalid workflow version")
	}

	workflow.gatekeeper = semaphore.NewWeighted(int64(options.Concurrency))
	workflow.options = options
	workflow.stopFlag = false
	workflow.signal = &sync.Mutex{}

	// validate depends on and link them to the step
	for idx, step := range workflow.Steps {
		workflow.Steps[idx].workflow = workflow
		for _, priorStepName := range step.DependsOn {
			priorStep := workflow.findStepByName(priorStepName)
			if priorStep == nil {
				return nil, fmt.Errorf("invalid step name in runs_after for step %s (%s)", step.Name, priorStepName)
			}

			workflow.Steps[idx].dependsOn = append(workflow.Steps[idx].dependsOn, priorStep)
		}
	}

	return workflow, nil
}

// LoadWorkflowFromReader loads a workflow from an io reader
func LoadWorkflowFromReader(ctx context.Context, options *WorkflowOptions, reader io.Reader) (*Workflow, error) {
	buff, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	return LoadWorkflowFromBytes(ctx, options, buff)
}

// Run runs the entire workflow
func (w *Workflow) Run(ctx context.Context) error {
	w.logger, ctx = LoggerContext(ctx)

	joiner := sync.WaitGroup{}

	// TODO: override if specified
	options := &StepOptions{
		Notifier: w.options.Notifier,
	}

	// Run all that can run
	for {
		if w.shouldStop(ctx) {
			return nil
		}
		if w.allDone() {
			break
		}

		step := w.nextToRun(ctx)
		if step == nil {
			continue
		}

		err := w.gatekeeper.Acquire(ctx, 1)
		if err != nil {
			return err
		}

		go func(toRun *Step) {
			joiner.Add(1)
			defer joiner.Done()

			toRun.options = options
			err := toRun.Run(ctx)
			if err != nil {
				// run railed in a way that the whole workflow should stop
				w.logger.Error(err)
				w.stop(ctx)
			}

			w.gatekeeper.Release(1)

		}(step)
	}

	joiner.Wait()

	return nil
}

// nextToRun returns the next step that can run
func (w *Workflow) nextToRun(ctx context.Context) *Step {
	// using a universal lock per workflow to pick the next step to run
	w.signal.Lock()
	defer w.signal.Unlock()

	for idx, step := range w.Steps {
		if step.shouldRun() {
			w.Steps[idx].MarkAsPending()
			return w.Steps[idx]
		}
	}

	return nil
}

func (w *Workflow) allDone() bool {
	w.signal.Lock()
	defer w.signal.Unlock()

	for _, step := range w.Steps {
		if !step.isDone() {
			return false
		}
	}

	return true
}

func (w *Workflow) findStepByName(name string) *Step {
	for idx, step := range w.Steps {
		if step.Name == name {
			return w.Steps[idx]
		}
	}

	return nil
}

func (w *Workflow) stop(ctx context.Context) {
	w.signal.Lock()
	defer w.signal.Unlock()

	w.stopFlag = true
}

func (w *Workflow) shouldStop(ctx context.Context) bool {
	w.signal.Lock()
	defer w.signal.Unlock()

	return w.stopFlag
}
