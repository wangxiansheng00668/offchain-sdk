package baseapp

import (
	"context"
	"os"
	"time"

	"github.com/berachain/offchain-sdk/job"
	"github.com/berachain/offchain-sdk/log"
	"github.com/berachain/offchain-sdk/worker"
)

type JobManager struct {
	// logger is the logger for the baseapp
	logger log.Logger

	// list of jobs
	jobs []job.Basic

	// Job producers are a pool of workers that produce jobs. These workers
	// run in the background and produce jobs that are then consumed by the
	// job executors.
	jobProducers worker.Pool

	// Job executors are a pool of workers that execute jobs. These workers
	// are fed jobs by the job producers.
	jobExecutors worker.Pool
}

// New creates a new baseapp.
func NewJobManager(
	name string,
	logger log.Logger,
	jobs []job.Basic,
) *JobManager {
	// TODO: read from config.
	poolCfg := worker.DefaultPoolConfig()
	poolCfg.Name = name
	poolCfg.PrometheusPrefix = "job_executor"
	return &JobManager{
		logger:       log.NewBlankLogger(os.Stdout),
		jobs:         jobs,
		jobExecutors: *worker.NewPool(poolCfg, logger),
		jobProducers: *worker.NewPool(&worker.PoolConfig{
			Name:             "job-producer",
			PrometheusPrefix: "job_producer",
			MinWorkers:       len(jobs),
			MaxWorkers:       len(jobs),
			ResizingStrategy: "balanced", // doesnt really matter, MinWkr == MaxWkr
			MaxQueuedJobs:    1000,       //nolint:gomnd // TODO: paramterize
		}, logger),
	}
}

// Start.
//
//nolint:gocognit // todo: fix.
func (jm *JobManager) Start(ctx context.Context) {
	for _, j := range jm.jobs {
		if err := j.Setup(ctx); err != nil {
			panic(err)
		}

		//nolint:nestif // todo fix.
		if condJob, ok := j.(job.Conditional); ok {
			jm.jobProducers.Submit(func() {
				for {
					time.Sleep(50 * time.Millisecond) //nolint:gomnd // fix.
					if condJob.Condition(ctx) {
						jm.jobExecutors.AddJob(job.NewPayload(ctx, condJob, nil))
						return
					}
				}
			})
		} else if subJob, ok := j.(job.Subscribable); ok { //nolint:govet // todo fix.
			jm.jobProducers.Submit(func() {
				ch := subJob.Subscribe(ctx)
				for {
					select {
					case val := <-ch:
						jm.jobExecutors.AddJob(job.NewPayload(ctx, subJob, val))
					case <-ctx.Done():
						return
					default:
						continue
					}
				}
			})
		} else if ethSubJob, ok := j.(job.EthSubscribable); ok { //nolint:govet // todo fix.
			jm.jobProducers.Submit(func() {
				sub, ch := ethSubJob.Subscribe(ctx)
				for {
					select {
					case <-ctx.Done():
						ethSubJob.Unsubscribe(ctx)
						return
					case err := <-sub.Err():
						jm.logger.Error("error in subscription", "err", err)
						// TODO: add retry mechanism
						ethSubJob.Unsubscribe(ctx)
						return
					case val := <-ch:
						jm.jobExecutors.AddJob(job.NewPayload(ctx, ethSubJob, val))
						continue
					}
				}
			})
		} else if pollingJob, ok := j.(job.Polling); ok { //nolint:govet // todo fix.
			jm.jobProducers.Submit(func() {
				for {
					time.Sleep(pollingJob.IntervalTime(ctx))
					jm.jobExecutors.AddJob(job.NewPayload(ctx, pollingJob, nil))
				}
			})
		} else {
			panic("unknown job type")
		}
	}
}

// Stop.
func (jm *JobManager) Stop() {
	for _, j := range jm.jobs {
		if err := j.Teardown(); err != nil {
			panic(err)
		}
	}
}
