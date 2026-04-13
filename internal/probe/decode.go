//go:build linux

package probe

import (
	"bytes"

	"github.com/devopyos/hi-drawbridge/internal/hidio"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func decodeFeatureFrame(
	frame []byte,
	candidate model.HidCandidate,
	p profile.ProfileSpec,
) *model.BatteryReading {
	if len(frame) < p.QueryLength {
		return nil
	}

	//nolint:gosec // query_report_id is validated as a byte during profile config loading.
	if frame[0] != byte(p.QueryReportID) {
		return nil
	}

	if len(p.ExpectedSignature) == 0 {
		return nil
	}

	if !bytes.Contains(frame, p.ExpectedSignature) {
		return nil
	}

	if p.BatteryOffset < 0 || p.StatusOffset < 0 {
		return nil
	}

	if p.BatteryOffset >= len(frame) || p.StatusOffset >= len(frame) {
		return nil
	}

	percentage := int(frame[p.BatteryOffset])
	if percentage > 100 {
		return nil
	}

	status := int(frame[p.StatusOffset])

	return &model.BatteryReading{
		DeviceID:      candidate.StableDeviceID,
		Name:          p.Name,
		Percentage:    percentage,
		Transport:     candidate.Transport,
		ProfileID:     p.ID,
		DeviceType:    p.DeviceType,
		IconName:      p.IconName,
		Source:        model.FrameSourceFeature,
		CandidatePath: candidate.Path,
		Status:        &status,
	}
}

func decodeInterruptFrame(
	frame []byte,
	candidate model.HidCandidate,
	p profile.ProfileSpec,
) *model.BatteryReading {
	if frame == nil || !hidio.IsValidInterruptFrame(frame, p) {
		return nil
	}

	if p.FallbackBatteryOffset < 0 || p.FallbackBatteryOffset >= len(frame) {
		return nil
	}

	rawValue := int(frame[p.FallbackBatteryOffset])

	var percentage int
	if p.FallbackBatteryBucketMax > 0 {
		maxBucket := p.FallbackBatteryBucketMax
		if rawValue > maxBucket {
			return nil
		}

		percentage = (rawValue*100 + maxBucket/2) / maxBucket
	} else {
		if rawValue > 100 {
			return nil
		}

		percentage = rawValue
	}

	return &model.BatteryReading{
		DeviceID:      candidate.StableDeviceID,
		Name:          p.Name,
		Percentage:    percentage,
		Transport:     candidate.Transport,
		ProfileID:     p.ID,
		DeviceType:    p.DeviceType,
		IconName:      p.IconName,
		Source:        model.FrameSourceInterrupt,
		CandidatePath: candidate.Path,
	}
}
