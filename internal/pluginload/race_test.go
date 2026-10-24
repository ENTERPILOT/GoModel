//go:build race

package pluginload

func init() { raceEnabled = true }
