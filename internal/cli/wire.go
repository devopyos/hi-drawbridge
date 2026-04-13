//go:build linux

package cli

import (
	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

type configOutput struct {
	ConfigExists        bool                   `json:"config_exists"`
	ConfigPath          string                 `json:"config_path"`
	ProfilesDir         string                 `json:"profiles_dir"`
	ProfilesDirExists   bool                   `json:"profiles_dir_exists"`
	ProfilesDirExplicit bool                   `json:"profiles_dir_explicit"`
	Profiles            []profile.ProfileDebug `json:"profiles"`
	SelectedProfileIDs  []string               `json:"selected_profile_ids"`
	Settings            config.Settings        `json:"settings"`
}

type bridgeRunDebugOutput struct {
	ProfileID            string                      `json:"profile_id"`
	Source               string                      `json:"source"`
	Devices              []model.BatteryDevice       `json:"devices"`
	Candidates           []model.HidCandidate        `json:"candidates"`
	Targets              []model.ProbeTarget         `json:"targets"`
	ProbeResults         []model.ProbeResult         `json:"probe_results"`
	BestReadings         []model.BatteryReading      `json:"best_readings"`
	DiscoveryDiagnostics []model.DiscoveryDiagnostic `json:"discovery_diagnostics"`
	UsedStale            bool                        `json:"used_stale"`
	Error                string                      `json:"error,omitempty"`
	Profile              *profile.ProfileDebug       `json:"profile,omitempty"`
}

type probeDebugOutput struct {
	ConfigExists        bool                   `json:"config_exists"`
	ConfigPath          string                 `json:"config_path"`
	ProfilesDir         string                 `json:"profiles_dir"`
	ProfilesDirExists   bool                   `json:"profiles_dir_exists"`
	ProfilesDirExplicit bool                   `json:"profiles_dir_explicit"`
	Devices             []model.BatteryDevice  `json:"devices"`
	ProfileResults      []bridgeRunDebugOutput `json:"profile_results"`
	SelectedProfileIDs  []string               `json:"selected_profile_ids"`
}

func buildProbeDebugOutput(rt *RuntimeContext, result model.MultiBridgeRunResult) probeDebugOutput {
	profileLookup := make(map[string]profile.ProfileDebug, len(rt.SelectedProfiles))
	selectedIDs := selectedProfileIDs(rt.SelectedProfiles)
	for _, p := range rt.SelectedProfiles {
		profileLookup[p.ID] = p.DebugOutput()
	}

	profileResults := make([]bridgeRunDebugOutput, 0, len(result.ProfileResults))
	for _, pr := range result.ProfileResults {
		profileResults = append(profileResults, bridgeRunResultToDebugOutput(pr, profileLookup))
	}

	return probeDebugOutput{
		ConfigExists:        rt.UserConfig.Exists,
		ConfigPath:          rt.UserConfig.Path,
		ProfilesDir:         rt.ProfilesDir,
		ProfilesDirExists:   rt.ProfilesDirExists,
		ProfilesDirExplicit: rt.ProfilesDirExplicit,
		Devices:             append([]model.BatteryDevice{}, result.Devices...),
		ProfileResults:      profileResults,
		SelectedProfileIDs:  selectedIDs,
	}
}

func buildConfigOutput(rt *RuntimeContext) configOutput {
	return configOutput{
		ConfigExists:        rt.UserConfig.Exists,
		ConfigPath:          rt.UserConfig.Path,
		ProfilesDir:         rt.ProfilesDir,
		ProfilesDirExists:   rt.ProfilesDirExists,
		ProfilesDirExplicit: rt.ProfilesDirExplicit,
		Profiles:            rt.ProfileRegistry.DebugOutput(),
		SelectedProfileIDs:  selectedProfileIDs(rt.SelectedProfiles),
		Settings:            rt.Settings,
	}
}

func bridgeRunResultToDebugOutput(r model.BridgeRunResult, profileLookup map[string]profile.ProfileDebug) bridgeRunDebugOutput {
	output := bridgeRunDebugOutput{
		ProfileID:            r.ProfileID,
		Source:               r.Source,
		Devices:              append([]model.BatteryDevice{}, r.Devices...),
		Candidates:           append([]model.HidCandidate{}, r.Candidates...),
		Targets:              append([]model.ProbeTarget{}, r.Targets...),
		ProbeResults:         append([]model.ProbeResult{}, r.ProbeResults...),
		BestReadings:         append([]model.BatteryReading{}, r.BestReadings...),
		DiscoveryDiagnostics: append([]model.DiscoveryDiagnostic{}, r.DiscoveryDiagnostics...),
		UsedStale:            r.UsedStale,
		Error:                errorString(r.Error),
	}

	if profileInfo, ok := profileLookup[r.ProfileID]; ok {
		profileCopy := profileInfo
		output.Profile = &profileCopy
	}

	return output
}

func selectedProfileIDs(profiles []profile.ProfileSpec) []string {
	ids := make([]string, 0, len(profiles))
	for _, p := range profiles {
		ids = append(ids, p.ID)
	}

	return ids
}

func errorString(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}
