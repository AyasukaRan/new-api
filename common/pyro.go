package common

import (
	"os"
	"runtime"
	"sync/atomic"

	"github.com/grafana/pyroscope-go"
)

var pyroscopeRunning atomic.Bool

func PyroscopeRunning() bool {
	return pyroscopeRunning.Load()
}

func StartPyroScope() error {

	pyroscopeUrl := GetEnvOrDefaultString("PYROSCOPE_URL", "")
	if pyroscopeUrl == "" && os.Getenv("ENABLE_PPROF") != "true" {
		return nil
	}

	// Keep lock/block sampling useful for both the continuous collector and
	// authenticated snapshots without sampling nearly every blocking event.
	mutexRate := max(0, GetEnvOrDefault("PYROSCOPE_MUTEX_RATE", 100))
	blockRate := max(0, GetEnvOrDefault("PYROSCOPE_BLOCK_RATE", 1_000_000))
	runtime.SetMutexProfileFraction(mutexRate)
	runtime.SetBlockProfileRate(blockRate)
	if pyroscopeUrl == "" {
		return nil
	}

	pyroscopeAppName := GetEnvOrDefaultString("PYROSCOPE_APP_NAME", "new-api")
	pyroscopeBasicAuthUser := GetEnvOrDefaultString("PYROSCOPE_BASIC_AUTH_USER", "")
	pyroscopeBasicAuthPassword := GetEnvOrDefaultString("PYROSCOPE_BASIC_AUTH_PASSWORD", "")
	pyroscopeHostname := GetEnvOrDefaultString("HOSTNAME", "new-api")

	_, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: pyroscopeAppName,

		ServerAddress:     pyroscopeUrl,
		BasicAuthUser:     pyroscopeBasicAuthUser,
		BasicAuthPassword: pyroscopeBasicAuthPassword,

		Logger: nil,
		// Observe natural collections instead of forcing a full GC every upload.
		DisableGCRuns: true,

		Tags: map[string]string{"hostname": pyroscopeHostname},

		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,

			pyroscope.ProfileGoroutines,
			pyroscope.ProfileMutexCount,
			pyroscope.ProfileMutexDuration,
			pyroscope.ProfileBlockCount,
			pyroscope.ProfileBlockDuration,
		},
	})
	if err != nil {
		return err
	}
	pyroscopeRunning.Store(true)
	return nil
}
