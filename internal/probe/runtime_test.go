//go:build linux

package probe

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/devopyos/hi-drawbridge/internal/config"
	"github.com/devopyos/hi-drawbridge/internal/model"
	"github.com/devopyos/hi-drawbridge/internal/profile"
)

func intPtr(v int) *int { return &v }

func testProfile(path model.ProbePath) profile.ProfileSpec {
	return profile.ProfileSpec{
		ID:                    "p1",
		Name:                  "Test Device",
		ProbePath:             path,
		QueryReportID:         0x51,
		QueryLength:           6,
		BatteryQueryCmd:       0x06,
		ExpectedSignature:     []byte{0xAA, 0xBB},
		BatteryOffset:         2,
		StatusOffset:          3,
		FallbackInputReportID: 0x54,
		FallbackInputCmd:      0xE4,
		FallbackInputLength:   5,
		FallbackBatteryOffset: 2,
		// 0..4 maps to 0..100; 5 is invalid for decodeInterruptFrame.
		FallbackBatteryBucketMax: 4,
		WakeReport:               []byte{0x00, 0x01, 0x00},
		DeviceType:               "mouse",
		IconName:                 "input-mouse",
	}
}

func testTarget(path string) model.ProbeTarget {
	candidate := model.HidCandidate{
		StableDeviceID: "dev1",
		HidrawName:     "hidraw0",
		Transport:      model.TransportUSBDirect,
		Path:           path,
	}

	return model.ProbeTarget{
		DeviceID:       "dev1",
		Transport:      candidate.Transport,
		QueryCandidate: candidate,
		WakeCandidate:  candidate,
	}
}

func defaultSettings() config.Settings {
	return config.Settings{
		PreferredTransports:    []model.Transport{model.TransportUSBDirect, model.TransportReceiver},
		RetryCount:             1,
		RetryDelayMs:           1,
		CacheTTLSec:            10,
		StaleTTLSec:            30,
		ServicePollIntervalSec: 10,
		LogLevel:               "DEBUG",
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestWaitWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitWithContext(ctx, 0) {
		t.Fatal("expected false with canceled context and zero delay")
	}

	ctx = context.Background()
	if !waitWithContext(ctx, 1*time.Millisecond) {
		t.Fatal("expected true when timer elapses")
	}
}

func TestSortAndRetryHelpers(t *testing.T) {
	if !lessCandidateSortKeys(
		candidateSortKey{rank: 0, interfaceNum: 1, path: "/a"},
		candidateSortKey{rank: 1, interfaceNum: 0, path: "/b"},
	) {
		t.Fatal("expected lower rank to sort first")
	}
	if !lessCandidateSortKeys(
		candidateSortKey{rank: 1, interfaceNum: 1, path: "/a"},
		candidateSortKey{rank: 1, interfaceNum: 2, path: "/b"},
	) {
		t.Fatal("expected lower interface number to sort first")
	}
	if !lessCandidateSortKeys(
		candidateSortKey{rank: 1, interfaceNum: 2, path: "/a"},
		candidateSortKey{rank: 1, interfaceNum: 2, path: "/b"},
	) {
		t.Fatal("expected lexical path order when rank/interface match")
	}

	if got := maxFeatureReads(profile.ProfileSpec{}); got != 2 {
		t.Fatalf("expected maxFeatureReads=2 without prime command, got %d", got)
	}
	if got := maxFeatureReads(profile.ProfileSpec{PrimeQueryCmd: intPtr(1)}); got != 3 {
		t.Fatalf("expected maxFeatureReads=3 with prime command, got %d", got)
	}

	state := &featureProbeState{featureReadsEnabled: true}
	if shouldRetryFeatureRead(1, 3, false, nil, state) {
		t.Fatal("expected no retry when interrupt fallback disabled")
	}
	if shouldRetryFeatureRead(1, 3, true, nil, &featureProbeState{featureReadsEnabled: false}) {
		t.Fatal("expected no retry when feature reads are disabled")
	}
	if shouldRetryFeatureRead(3, 3, true, nil, &featureProbeState{featureReadsEnabled: true}) {
		t.Fatal("expected no retry on final attempt")
	}

	state = &featureProbeState{featureReadsEnabled: true}
	if !shouldRetryFeatureRead(1, 3, true, []byte{0x01}, state) {
		t.Fatal("expected retry for invalid feature frame before final attempt")
	}
	typed := &FeatureProbeError{}
	if !errors.As(state.lastError, &typed) || typed.Code != FeatureProbeErrorFrameInvalid {
		t.Fatalf("expected feature frame invalid code, got %v", state.lastError)
	}

	state = &featureProbeState{featureReadsEnabled: true}
	if !shouldRetryFeatureRead(1, 3, true, nil, state) {
		t.Fatal("expected retry for empty frame before final attempt")
	}
	if !errors.As(state.lastError, &typed) || typed.Code != FeatureProbeErrorNoValidFrame {
		t.Fatalf("expected no valid frame code, got %v", state.lastError)
	}
}

func TestRunPrimeFeatureQueryBranches(t *testing.T) {
	origSendQuery := probeSendQuery
	origRecv := probeRecvFeatureReport
	defer func() {
		probeSendQuery = origSendQuery
		probeRecvFeatureReport = origRecv
	}()

	// prime command absent
	state := &featureProbeState{featureReadsEnabled: true}
	runPrimeFeatureQuery(context.Background(), 10, profile.ProfileSpec{}, state)
	if state.lastError != nil {
		t.Fatalf("expected no error without prime query command, got %v", state.lastError)
	}

	// context already canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sendCalled := false
	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		sendCalled = true
		return nil, nil
	}
	runPrimeFeatureQuery(ctx, 10, profile.ProfileSpec{PrimeQueryCmd: intPtr(1)}, state)
	if sendCalled {
		t.Fatal("expected canceled context to skip prime query send")
	}

	// send error path
	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		return nil, errors.New("prime send failed")
	}
	state = &featureProbeState{featureReadsEnabled: true}
	runPrimeFeatureQuery(context.Background(), 10, profile.ProfileSpec{PrimeQueryCmd: intPtr(1)}, state)
	if state.lastError == nil || !strings.Contains(state.lastError.Error(), "prime send failed") {
		t.Fatalf("expected send error to be recorded, got %v", state.lastError)
	}

	// recv error path with EPIPE
	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		return nil, nil
	}
	probeRecvFeatureReport = func(context.Context, int, byte, int) ([]byte, error) {
		return nil, unix.EPIPE
	}
	p := testProfile(model.ProbePathFeatureOnly)
	p.PrimeQueryCmd = intPtr(0x07)
	state = &featureProbeState{featureReadsEnabled: true}
	runPrimeFeatureQuery(context.Background(), 10, p, state)
	if state.featureReadsEnabled {
		t.Fatal("expected EPIPE to disable feature reads")
	}
}

func TestRunFeatureAttemptContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	state := &featureProbeState{featureReadsEnabled: true}
	_, status := runFeatureAttempt(
		ctx,
		10,
		model.HidCandidate{},
		defaultSettings(),
		testProfile(model.ProbePathFeatureOnly),
		1,
		false,
		state,
	)
	if status != featureAttemptStatusStop {
		t.Fatalf("expected stop status, got %v", status)
	}
	if !errors.Is(state.lastError, context.Canceled) {
		t.Fatalf("expected context cancellation error, got %v", state.lastError)
	}
}

func TestRunFeatureAttemptSendQueryError(t *testing.T) {
	origSendQuery := probeSendQuery
	defer func() { probeSendQuery = origSendQuery }()

	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		return nil, errors.New("query failed")
	}

	state := &featureProbeState{featureReadsEnabled: true}
	_, status := runFeatureAttempt(
		context.Background(),
		10,
		model.HidCandidate{},
		defaultSettings(),
		testProfile(model.ProbePathFeatureOnly),
		1,
		false,
		state,
	)
	if status != featureAttemptStatusRetry {
		t.Fatalf("expected retry status, got %v", status)
	}
	if state.lastError == nil || !strings.Contains(state.lastError.Error(), "query failed") {
		t.Fatalf("expected query error, got %v", state.lastError)
	}
}

func TestRunFeatureAttemptFeatureSuccess(t *testing.T) {
	origSendQuery := probeSendQuery
	origRecv := probeRecvFeatureReport
	defer func() {
		probeSendQuery = origSendQuery
		probeRecvFeatureReport = origRecv
	}()

	p := testProfile(model.ProbePathFeatureOnly)
	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		return nil, nil
	}
	probeRecvFeatureReport = func(context.Context, int, byte, int) ([]byte, error) {
		return []byte{0x51, 0x00, 55, 1, 0xAA, 0xBB}, nil
	}

	state := &featureProbeState{featureReadsEnabled: true}
	reading, status := runFeatureAttempt(
		context.Background(),
		10,
		model.HidCandidate{StableDeviceID: "dev1", Transport: model.TransportUSBDirect, Path: "/dev/hidraw0"},
		defaultSettings(),
		p,
		1,
		false,
		state,
	)
	if status != featureAttemptStatusSuccess {
		t.Fatalf("expected success status, got %v", status)
	}
	if reading == nil || reading.Percentage != 55 {
		t.Fatalf("expected decoded reading, got %#v", reading)
	}
}

func TestRunFeatureAttemptEPIPEDisablesFeatureRead(t *testing.T) {
	origSendQuery := probeSendQuery
	origRecv := probeRecvFeatureReport
	defer func() {
		probeSendQuery = origSendQuery
		probeRecvFeatureReport = origRecv
	}()

	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		return nil, nil
	}
	probeRecvFeatureReport = func(context.Context, int, byte, int) ([]byte, error) {
		return nil, unix.EPIPE
	}

	state := &featureProbeState{featureReadsEnabled: true}
	_, status := runFeatureAttempt(
		context.Background(),
		10,
		model.HidCandidate{},
		defaultSettings(),
		testProfile(model.ProbePathFeatureOnly),
		1,
		false,
		state,
	)
	if status != featureAttemptStatusRetry {
		t.Fatalf("expected retry status, got %v", status)
	}
	if state.featureReadsEnabled {
		t.Fatal("expected feature reads to be disabled after EPIPE")
	}

	typed := &FeatureProbeError{}
	if !errors.As(state.lastError, &typed) || typed.Code != FeatureProbeErrorReadEPIPE {
		t.Fatalf("expected read epipe code, got %v", state.lastError)
	}
}

func TestRunFeatureAttemptInterruptInvalidCode(t *testing.T) {
	origSendQuery := probeSendQuery
	origRecv := probeRecvFeatureReport
	origInterrupt := probeReadInterruptFrame
	defer func() {
		probeSendQuery = origSendQuery
		probeRecvFeatureReport = origRecv
		probeReadInterruptFrame = origInterrupt
	}()

	p := testProfile(model.ProbePathFeatureOrInterrupt)
	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		return nil, nil
	}
	probeRecvFeatureReport = func(context.Context, int, byte, int) ([]byte, error) {
		// Valid report id but invalid signature for decode.
		return []byte{0x51, 0x00, 10, 1, 0x00, 0x00}, nil
	}
	probeReadInterruptFrame = func(context.Context, int, profile.ProfileSpec, int) ([]byte, error) {
		// Valid interrupt frame shape, but battery bucket overflow so decode returns nil.
		return []byte{0x54, 0xE4, 0x05, 0x00, 0x00}, nil
	}

	state := &featureProbeState{featureReadsEnabled: true}
	_, status := runFeatureAttempt(
		context.Background(),
		10,
		model.HidCandidate{},
		defaultSettings(),
		p,
		1, // final attempt
		true,
		state,
	)
	if status != featureAttemptStatusRetry {
		t.Fatalf("expected retry status, got %v", status)
	}

	typed := &FeatureProbeError{}
	if !errors.As(state.lastError, &typed) || typed.Code != FeatureProbeErrorInterruptFrameInvalid {
		t.Fatalf("expected interrupt invalid code, got %v", state.lastError)
	}
}

func TestProbeWithFeatureReadsInterruptOnlyOnceOnFinalAttempt(t *testing.T) {
	origSendQuery := probeSendQuery
	origRecv := probeRecvFeatureReport
	origInterrupt := probeReadInterruptFrame
	defer func() {
		probeSendQuery = origSendQuery
		probeRecvFeatureReport = origRecv
		probeReadInterruptFrame = origInterrupt
	}()

	settings := defaultSettings()
	p := testProfile(model.ProbePathFeatureOrInterrupt)
	target := testTarget("/dev/hidraw0")

	probeSendQuery = func(context.Context, int, int, profile.ProfileSpec) ([]byte, error) {
		return nil, nil
	}
	probeRecvFeatureReport = func(context.Context, int, byte, int) ([]byte, error) {
		return []byte{0x51, 0x00, 10, 1, 0x00, 0x00}, nil
	}
	interruptReads := 0
	probeReadInterruptFrame = func(context.Context, int, profile.ProfileSpec, int) ([]byte, error) {
		interruptReads++
		return nil, nil
	}

	result := probeWithFeature(context.Background(), 10, target, settings, p, discardLogger(), nil, nil)
	if result.Success {
		t.Fatalf("expected failure result, got %#v", result)
	}
	if interruptReads != 1 {
		t.Fatalf("expected a single interrupt fallback read, got %d", interruptReads)
	}
}

func TestProbeWithOutputSetsInterruptInvalidError(t *testing.T) {
	origSendOutput := probeSendOutputQuery
	origInterrupt := probeReadInterruptFrame
	defer func() {
		probeSendOutputQuery = origSendOutput
		probeReadInterruptFrame = origInterrupt
	}()

	settings := defaultSettings()
	p := testProfile(model.ProbePathInterruptOnly)
	target := testTarget("/dev/hidraw0")

	probeSendOutputQuery = func(context.Context, int, int, profile.ProfileSpec) (int, error) {
		return 2, nil
	}
	probeReadInterruptFrame = func(context.Context, int, profile.ProfileSpec, int) ([]byte, error) {
		return []byte{0x54, 0xE4, 0x05, 0x00, 0x00}, nil
	}

	result := probeWithOutput(context.Background(), 10, target, settings, p, discardLogger(), nil, nil)
	if result.Success {
		t.Fatalf("expected failure result, got %#v", result)
	}

	typed := &FeatureProbeError{}
	if !errors.As(result.Error, &typed) || typed.Code != FeatureProbeErrorInterruptFrameInvalid {
		t.Fatalf("expected interrupt invalid error code, got %v", result.Error)
	}
}

func TestProbeWithOutputSuccessAndSendError(t *testing.T) {
	origSendOutput := probeSendOutputQuery
	origInterrupt := probeReadInterruptFrame
	defer func() {
		probeSendOutputQuery = origSendOutput
		probeReadInterruptFrame = origInterrupt
	}()

	settings := defaultSettings()
	p := testProfile(model.ProbePathInterruptOnly)
	target := testTarget("/dev/hidraw0")

	probeSendOutputQuery = func(context.Context, int, int, profile.ProfileSpec) (int, error) {
		return 2, nil
	}
	probeReadInterruptFrame = func(context.Context, int, profile.ProfileSpec, int) ([]byte, error) {
		return []byte{0x54, 0xE4, 0x02, 0x00, 0x00}, nil
	}
	success := probeWithOutput(context.Background(), 10, target, settings, p, discardLogger(), nil, nil)
	if !success.Success || success.Reading == nil {
		t.Fatalf("expected interrupt output success, got %#v", success)
	}

	probeSendOutputQuery = func(context.Context, int, int, profile.ProfileSpec) (int, error) {
		return 0, errors.New("send output failed")
	}
	failed := probeWithOutput(context.Background(), 10, target, settings, p, discardLogger(), nil, nil)
	if failed.Success {
		t.Fatalf("expected failure when output send fails, got %#v", failed)
	}
	if failed.Error == nil || !strings.Contains(failed.Error.Error(), "send output failed") {
		t.Fatalf("expected send error, got %v", failed.Error)
	}
}

func TestSendWakeNilLoggerNoPanic(t *testing.T) {
	origWrite := probeWrite
	defer func() { probeWrite = origWrite }()

	probeWrite = func(int, []byte) (int, error) {
		return 0, unix.EIO
	}

	err := sendWake(10, "/dev/hidraw0", testProfile(model.ProbePathFeatureOnly), nil)
	if err == nil {
		t.Fatal("expected wake write error")
	}
}

func TestSendWakeSuccessAndShortWrite(t *testing.T) {
	origWrite := probeWrite
	defer func() { probeWrite = origWrite }()

	p := testProfile(model.ProbePathFeatureOnly)
	probeWrite = func(int, []byte) (int, error) {
		return len(p.WakeReport), nil
	}
	if err := sendWake(10, "/dev/hidraw0", p, discardLogger()); err != nil {
		t.Fatalf("expected wake success, got %v", err)
	}

	probeWrite = func(int, []byte) (int, error) {
		return len(p.WakeReport) - 1, nil
	}
	if err := sendWake(10, "/dev/hidraw0", p, discardLogger()); err == nil {
		t.Fatal("expected short wake write error")
	}
}

func TestProbeTargetOpenFailures(t *testing.T) {
	origOpen := probeOpen
	origClose := probeClose
	defer func() {
		probeOpen = origOpen
		probeClose = origClose
	}()

	settings := defaultSettings()
	p := testProfile(model.ProbePathFeatureOnly)
	target := testTarget("/dev/query")
	target.WakeCandidate.Path = "/dev/wake"

	probeOpen = func(path string, flag int, perm uint32) (int, error) {
		return -1, unix.EACCES
	}
	result := ProbeTarget(context.Background(), target, settings, p, nil)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "open query fd") {
		t.Fatalf("expected open query fd error, got %v", result.Error)
	}

	openCalls := 0
	closeCalls := 0
	probeOpen = func(path string, flag int, perm uint32) (int, error) {
		openCalls++
		if openCalls == 1 {
			return 100, nil
		}
		return -1, unix.EBUSY
	}
	probeClose = func(fd int) error {
		closeCalls++
		return nil
	}

	result = ProbeTarget(context.Background(), target, settings, p, nil)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "open wake fd") {
		t.Fatalf("expected open wake fd error, got %v", result.Error)
	}
	if closeCalls != 1 {
		t.Fatalf("expected one close for query fd on wake open failure, got %d", closeCalls)
	}
}

func TestProbeTargetPassiveSuccess(t *testing.T) {
	origOpen := probeOpen
	origClose := probeClose
	origPoll := probePoll
	origRead := probeRead
	defer func() {
		probeOpen = origOpen
		probeClose = origClose
		probePoll = origPoll
		probeRead = origRead
	}()

	settings := defaultSettings()
	p := testProfile(model.ProbePathPassive)
	target := testTarget("/dev/hidraw0")

	probeOpen = func(path string, flag int, perm uint32) (int, error) { return 100, nil }
	closeCalls := 0
	probeClose = func(fd int) error {
		closeCalls++
		return nil
	}
	probePoll = func(fds []unix.PollFd, timeout int) (int, error) {
		fds[0].Revents = unix.POLLIN
		return 1, nil
	}
	probeRead = func(fd int, buf []byte) (int, error) {
		frame := []byte{0x54, 0xE4, 0x02, 0x00, 0x00}
		copy(buf, frame)
		return len(frame), nil
	}

	result := ProbeTarget(context.Background(), target, settings, p, discardLogger())
	if !result.Success || result.Reading == nil {
		t.Fatalf("expected passive probe success, got %#v", result)
	}
	if closeCalls != 1 {
		t.Fatalf("expected one fd close, got %d", closeCalls)
	}
}

func TestProbePassivePaths(t *testing.T) {
	origPoll := probePoll
	origRead := probeRead
	defer func() {
		probePoll = origPoll
		probeRead = origRead
	}()

	settings := defaultSettings()
	p := testProfile(model.ProbePathPassive)
	target := testTarget("/dev/hidraw0")

	// Success path.
	probePoll = func(fds []unix.PollFd, timeout int) (int, error) {
		fds[0].Revents = unix.POLLIN
		return 1, nil
	}
	probeRead = func(fd int, buf []byte) (int, error) {
		frame := []byte{0x54, 0xE4, 0x02, 0x00, 0x00}
		copy(buf, frame)
		return len(frame), nil
	}
	success := probePassive(context.Background(), 10, target, settings, p, discardLogger(), nil, nil)
	if !success.Success || success.Reading == nil {
		t.Fatalf("expected passive success, got %#v", success)
	}

	// Poll flag error path.
	probePoll = func(fds []unix.PollFd, timeout int) (int, error) {
		fds[0].Revents = unix.POLLNVAL
		return 1, nil
	}
	failure := probePassive(context.Background(), 10, target, settings, p, discardLogger(), nil, nil)
	if failure.Error == nil || !strings.Contains(failure.Error.Error(), "invalid fd") {
		t.Fatalf("expected passive poll error, got %v", failure.Error)
	}
}

func TestCollectBestReadingsWithProbeFailure(t *testing.T) {
	origOpen := probeOpen
	defer func() { probeOpen = origOpen }()

	probeOpen = func(path string, flag int, perm uint32) (int, error) {
		return -1, unix.EIO
	}

	settings := defaultSettings()
	p := testProfile(model.ProbePathPassive)
	candidates := []model.HidCandidate{
		{
			StableDeviceID: "dev1",
			Transport:      model.TransportUSBDirect,
			Path:           "/dev/hidraw0",
		},
	}

	readings, results := CollectBestReadings(context.Background(), candidates, settings, p, discardLogger())
	if len(readings) != 0 {
		t.Fatalf("expected no readings on probe failure, got %#v", readings)
	}
	if len(results) != 1 {
		t.Fatalf("expected one probe result, got %d", len(results))
	}
	if results[0].Error == nil {
		t.Fatal("expected probe result error")
	}
}
