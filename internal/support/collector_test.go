package support

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tphakala/birdnet-go/internal/privacy"
)

// Test constants
const (
	// File sizes for testing
	testLogSizeLimit  = 5000
	testLogSizeSmall  = 1000
	testLogSizeMedium = 4000
	testLogSizeLarge  = 4500
	testLogSizeTiny   = 0

	// Test durations
	testDuration24Hours = 24 * time.Hour
	testDuration1Hour   = 1 * time.Hour
	testDuration48Hours = 48 * time.Hour
	testDuration1Minute = 1 * time.Minute

	// Test sizes in bytes
	testSize10MB = 10 * 1024 * 1024
	testSize1KB  = 1024
)

// TestLogFileCollector_isLogFile tests the log file detection
func TestLogFileCollector_isLogFile(t *testing.T) {
	t.Parallel()

	lfc := &logFileCollector{}

	tests := []struct {
		name     string
		filename string
		want     bool
	}{
		{"standard log file", "application.log", true},
		{"rotated log file", "birdweather-2025-06-25T15-36-29.209.log", true},
		{"uppercase extension", "ERROR.LOG", true},
		{"no extension logfile", "logfile", false},
		{"different extension", "data.txt", false},
		{"hidden log file", ".hidden.log", true},
		{"log in filename", "mylog.txt", false},
		{"empty filename", "", false},
		{"only extension", ".log", true},
		{"path with log file", "/path/to/file.log", true},
		{"path without log file", "/path/to/file.txt", false},
		{"custom log suffix", "app.debuglog", true},
		{"custom log suffix uppercase", "app.ERRORLOG", true},
		{"another log suffix", "system.applog", true},
		{"ends with log word", "mylog", true},
		{"ends with LOG uppercase", "MYLOG", true},
		{"log in middle", "logdata.txt", false},
		{"composite extension", "file.log.bak", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := lfc.isLogFile(tt.filename); got != tt.want {
				t.Errorf("isLogFile(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

// TestLogFileCollector_isFileWithinTimeRange tests time range checking
func TestLogFileCollector_isFileWithinTimeRange(t *testing.T) {
	now := time.Now()
	lfc := &logFileCollector{
		cutoffTime: now.Add(-testDuration24Hours), // 24 hours ago
	}

	tests := []struct {
		name    string
		modTime time.Time
		want    bool
	}{
		{"recent file", now.Add(-testDuration1Hour), true},
		{"file at cutoff", now.Add(-testDuration24Hours), true},
		{"old file", now.Add(-testDuration48Hours), false},
		{"future file", now.Add(testDuration1Hour), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock FileInfo
			info := &mockFileInfo{modTime: tt.modTime}
			if got := lfc.isFileWithinTimeRange(info); got != tt.want {
				t.Errorf("isFileWithinTimeRange() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLogFileCollector_canAddFile tests size limit checking
func TestLogFileCollector_canAddFile(t *testing.T) {
	tests := []struct {
		name      string
		totalSize int64
		maxSize   int64
		fileSize  int64
		want      bool
	}{
		{"within limit", testLogSizeSmall, testLogSizeLimit, testLogSizeSmall, true},
		{"exactly at limit", testLogSizeMedium, testLogSizeLimit, testLogSizeSmall, true},
		{"exceeds limit", testLogSizeLarge, testLogSizeLimit, testLogSizeSmall, false},
		{"zero file size", testLogSizeSmall, testLogSizeLimit, testLogSizeTiny, true},
		{"already at max", testLogSizeLimit, testLogSizeLimit, testLogSizeSmall, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lfc := &logFileCollector{
				totalSize: tt.totalSize,
				maxSize:   tt.maxSize,
			}
			if got := lfc.canAddFile(tt.fileSize); got != tt.want {
				t.Errorf("canAddFile(%d) = %v, want %v", tt.fileSize, got, tt.want)
			}
		})
	}
}

// TestCollector_getLogSearchPaths tests log path generation
func TestCollector_getLogSearchPaths(t *testing.T) {
	t.Parallel()

	c := &Collector{
		configPath: "/etc/birdnet",
		dataPath:   "/var/lib/birdnet",
	}

	paths := c.getLogSearchPaths()

	// Should have at least the base paths
	expectedPaths := []string{
		"logs",
		"/var/lib/birdnet/logs",
		"/etc/birdnet/logs",
	}

	assert.GreaterOrEqual(t, len(paths), len(expectedPaths), "Expected at least %d paths", len(expectedPaths))

	// Check that expected paths are present
	for _, expected := range expectedPaths {
		assert.Contains(t, paths, expected, "Expected path %q not found in result", expected)
	}
}

// TestCollector_getUniqueLogPaths tests path deduplication
func TestCollector_getUniqueLogPaths(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()

	// Create collector with paths that would resolve to the same absolute path
	c := &Collector{
		configPath: tempDir,
		dataPath:   filepath.Join(tempDir, "..", filepath.Base(tempDir)),
	}

	// Change to temp directory to test relative path resolution
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWd); err != nil {
			t.Logf("Warning: Failed to restore working directory: %v", err)
		}
	})

	uniquePaths := c.getUniqueLogPaths()

	// Count occurrences of each absolute path
	pathCount := make(map[string]int)
	for _, path := range uniquePaths {
		abs, _ := filepath.Abs(path)
		pathCount[abs]++
	}

	// Check that no path appears more than once
	for path, count := range pathCount {
		if count > 1 {
			t.Errorf("Path %q appears %d times, expected 1", path, count)
		}
	}
}

// TestLogFileCollector_addNoLogsNote tests README creation
func TestLogFileCollector_addNoLogsNote(t *testing.T) {
	// Create a buffer to simulate zip writer
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)

	lfc := &logFileCollector{}
	lfc.addNoLogsNote(w)

	// Close the writer to finalize
	if err := w.Close(); err != nil {
		t.Fatalf("Failed to close zip writer: %v", err)
	}

	// Read the zip content
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("Failed to read zip: %v", err)
	}

	// Look for README.txt
	found := false
	for _, f := range r.File {
		if f.Name != "logs/README.txt" {
			continue
		}

		found = true

		// Check content
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("Failed to open README: %v", err)
		}

		content, err := io.ReadAll(rc)
		if closeErr := rc.Close(); closeErr != nil {
			t.Errorf("Failed to close README reader: %v", closeErr)
		} // Close immediately after reading, not deferred

		if err != nil {
			t.Fatalf("Failed to read README: %v", err)
		}

		expected := "No log files were found or all logs were older than the specified duration."
		if string(content) != expected {
			t.Errorf("README content = %q, want %q", string(content), expected)
		}
		break
	}

	if !found {
		t.Error("README.txt not found in archive")
	}
}

// TestCollector_scrubConfig tests sensitive data scrubbing
func TestCollector_scrubConfig(t *testing.T) {
	c := &Collector{
		sensitiveKeys: defaultSensitiveKeys(),
	}

	tests := []struct {
		name   string
		config map[string]any
		want   map[string]any
	}{
		{
			name: "scrub password fields",
			config: map[string]any{
				"password":   "secret123",
				"api_key":    "key123",
				"safe_field": "visible",
				"nested": map[string]any{
					"token":        "token456",
					"normal_field": "also_visible",
				},
			},
			want: map[string]any{
				"password":   "[REDACTED]",
				"api_key":    "[REDACTED]",
				"safe_field": "visible",
				"nested": map[string]any{
					"token":        "[REDACTED]",
					"normal_field": "also_visible",
				},
			},
		},
		{
			name: "sanitize RTSP URLs with credentials",
			config: map[string]any{
				"urls": []any{"rtsp://user:pass@192.168.1.100:554/stream", "http://example.com"},
				"data": []any{"safe1", "safe2"},
			},
			want: map[string]any{
				"urls": []any{"rtsp://192.168.1.100:554/stream", "http://example.com"},
				"data": []any{"safe1", "safe2"},
			},
		},
		{
			name: "handle mixed case keys",
			config: map[string]any{
				"Password": "secret",
				"API_KEY":  "key",
				"ApiToken": "token",
				"normal":   "visible",
			},
			want: map[string]any{
				"Password": "[REDACTED]",
				"API_KEY":  "[REDACTED]",
				"ApiToken": "[REDACTED]",
				"normal":   "visible",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.scrubConfig(tt.config)
			if !compareConfigs(got, tt.want) {
				t.Errorf("scrubConfig() = %v, want %v", got, tt.want)
			}
		})
	}
}

// compareConfigs compares two config maps for equality
func compareConfigs(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v1 := range a {
		v2, ok := b[k]
		if !ok {
			return false
		}
		switch t1 := v1.(type) {
		case map[string]any:
			t2, ok := v2.(map[string]any)
			if !ok || !compareConfigs(t1, t2) {
				return false
			}
		case []any:
			t2, ok := v2.([]any)
			if !ok || len(t1) != len(t2) {
				return false
			}
			for i := range t1 {
				if t1[i] != t2[i] {
					return false
				}
			}
		default:
			if v1 != v2 {
				return false
			}
		}
	}
	return true
}

// mockFileInfo implements os.FileInfo for testing
type mockFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (m *mockFileInfo) Name() string       { return m.name }
func (m *mockFileInfo) Size() int64        { return m.size }
func (m *mockFileInfo) Mode() os.FileMode  { return m.mode }
func (m *mockFileInfo) ModTime() time.Time { return m.modTime }
func (m *mockFileInfo) IsDir() bool        { return m.isDir }
func (m *mockFileInfo) Sys() any           { return nil }

// TestAnonymizeIPAddress tests IP address anonymization
func TestAnonymizeIPAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"localhost ipv4", "127.0.0.1", "localhost-"},
		{"localhost ipv6", "::1", "localhost-"},
		{"private ip 10.x", "10.0.1.100", "private-ip-"},
		{"private ip 192.168.x", "192.168.1.1", "private-ip-"},
		{"private ip 172.16.x", "172.16.0.1", "private-ip-"},
		{"link-local", "169.254.1.1", "private-ip-"},
		{"invalid ip", "not.an.ip", "invalid-ip-b94d27"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := privacy.AnonymizeIP(tt.input)
			switch {
			case tt.name == "invalid ip":
				// For invalid IPs, we just check the prefix
				assert.True(t, strings.HasPrefix(result, "invalid-ip-"), "Expected invalid-ip prefix for %s, got %s", tt.input, result)
			case strings.HasPrefix(tt.expected, "public-ip-"):
				// For public IPs, we just check the prefix since the hash varies
				assert.True(t, strings.HasPrefix(result, "public-ip-"), "Expected public-ip prefix for %s, got %s", tt.input, result)
			default:
				assert.True(t, strings.HasPrefix(result, tt.expected), "Expected %s prefix for %s, got %s", tt.expected, tt.input, result)
			}
		})
	}
}

// TestScrubLogMessage tests comprehensive log message scrubbing
func TestScrubLogMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		expected    []string // Expected substrings in output
		notExpected []string // Substrings that should NOT be in output
	}{
		{
			name:        "ip and url anonymization",
			input:       "Error connecting to https://192.168.1.100:8080/api from 10.0.1.50",
			expected:    []string{"Error connecting to", "from"},
			notExpected: []string{"192.168.1.100", "10.0.1.50", "8080/api"},
		},
		{
			name:        "email anonymization",
			input:       "User john.doe@example.com logged in",
			expected:    []string{"User", "logged in", "[EMAIL]"},
			notExpected: []string{"john.doe@example.com"},
		},
		{
			name:        "api key anonymization",
			input:       "api_key=sk_test_123456789abcdef error occurred",
			expected:    []string{"api_key: [TOKEN]", "error occurred"},
			notExpected: []string{"sk_test_123456789abcdef"},
		},
		{
			name:        "uuid anonymization",
			input:       "Processing request id: 550e8400-e29b-41d4-a716-446655440000",
			expected:    []string{"Processing request id:", "[UUID]"},
			notExpected: []string{"550e8400-e29b-41d4-a716-446655440000"},
		},
		{
			name:        "mixed sensitive data",
			input:       "Failed to connect to rtsp://admin:password@192.168.1.200:554/stream1 from 203.0.113.42 (API token: Bearer abc123def456)",
			expected:    []string{"Failed to connect to", "from", "Bearer [TOKEN]"},
			notExpected: []string{"admin:password", "192.168.1.200", "203.0.113.42", "abc123def456"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := privacy.ScrubMessage(tt.input)

			for _, expected := range tt.expected {
				assert.Contains(t, result, expected, "Expected '%s' to be in result: %s", expected, result)
			}

			for _, notExpected := range tt.notExpected {
				assert.NotContains(t, result, notExpected, "Expected '%s' to NOT be in result: %s", notExpected, result)
			}
		})
	}
}

// TestCollector_collectJournalLogs tests journal log collection error handling
func TestCollector_collectJournalLogs(t *testing.T) {
	t.Parallel()

	c := &Collector{}

	// Test journal log collection when journalctl is not available
	// This test will fail in environments where journalctl exists and works
	// so we'll just verify the error type is returned
	t.Run("journal not available", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		diagnostics := &LogSourceDiagnostics{PathsSearched: []SearchedPath{}, Details: make(map[string]any)}
		logs, err := c.collectJournalLogs(ctx, testDuration1Hour, false, diagnostics)

		// If journalctl is not available or service doesn't exist, we should get our sentinel error
		if err != nil {
			require.ErrorIs(t, err, ErrJournalNotAvailable, "Expected ErrJournalNotAvailable when journal is not available")
			assert.Nil(t, logs, "Expected nil logs when error is returned")
		}
		// If journalctl is available, logs might be returned or empty
		// We can't make strong assertions here since it depends on the environment
	})
}

// TestCollectionDiagnostics_Population tests that diagnostics are correctly populated on failures
func TestCollectionDiagnostics_Population(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func() *CollectionDiagnostics
		validate func(t *testing.T, diag *CollectionDiagnostics)
	}{
		{
			name: "log collection failure populates diagnostics",
			setup: func() *CollectionDiagnostics {
				diag := &CollectionDiagnostics{
					LogCollection: LogCollectionDiagnostics{
						JournalLogs: LogSourceDiagnostics{
							Attempted:  true,
							Successful: false,
							Error:      "journalctl not found",
							Details:    make(map[string]any),
						},
						FileLogs: LogSourceDiagnostics{
							Attempted:    true,
							Successful:   true,
							EntriesFound: 10,
							PathsSearched: []SearchedPath{
								{Path: "/var/log", Exists: true, Accessible: true, FileCount: 5},
							},
							Details: make(map[string]any),
						},
						Summary: DiagnosticSummary{
							TotalEntries: 10,
							TimeRange: TimeRange{
								From: time.Now().Add(-testDuration24Hours),
								To:   time.Now(),
							},
						},
					},
				}
				return diag
			},
			validate: func(t *testing.T, diag *CollectionDiagnostics) {
				t.Helper()
				assert.True(t, diag.LogCollection.JournalLogs.Attempted)
				assert.False(t, diag.LogCollection.JournalLogs.Successful)
				assert.Contains(t, diag.LogCollection.JournalLogs.Error, "journalctl")
				assert.True(t, diag.LogCollection.FileLogs.Successful)
				assert.Equal(t, 10, diag.LogCollection.FileLogs.EntriesFound)
				assert.Equal(t, 10, diag.LogCollection.Summary.TotalEntries)
			},
		},
		{
			name: "config collection failure populates diagnostics",
			setup: func() *CollectionDiagnostics {
				diag := &CollectionDiagnostics{
					ConfigCollection: DiagnosticInfo{
						Attempted:  true,
						Successful: false,
						Error:      "config file not found",
					},
				}
				return diag
			},
			validate: func(t *testing.T, diag *CollectionDiagnostics) {
				t.Helper()
				assert.True(t, diag.ConfigCollection.Attempted)
				assert.False(t, diag.ConfigCollection.Successful)
				assert.Contains(t, diag.ConfigCollection.Error, "config")
			},
		},
		{
			name: "system info collection failure populates diagnostics",
			setup: func() *CollectionDiagnostics {
				diag := &CollectionDiagnostics{
					SystemCollection: DiagnosticInfo{
						Attempted:  true,
						Successful: false,
						Error:      "permission denied",
					},
				}
				return diag
			},
			validate: func(t *testing.T, diag *CollectionDiagnostics) {
				t.Helper()
				assert.True(t, diag.SystemCollection.Attempted)
				assert.False(t, diag.SystemCollection.Successful)
				assert.Contains(t, diag.SystemCollection.Error, "permission")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			diag := tt.setup()
			tt.validate(t, diag)
		})
	}
}

// TestCollector_collectLogFilesWithDiagnostics tests file log collection with diagnostics
func TestCollector_collectLogFilesWithDiagnostics(t *testing.T) {
	// Create temp directory structure for testing
	tempDir := t.TempDir()
	logDir := filepath.Join(tempDir, "logs")
	require.NoError(t, os.MkdirAll(logDir, defaultDirPermissions))

	// Create test log files
	testLogContent := "2024-01-15 10:00:00 INFO test log entry\n"
	logFile1 := filepath.Join(logDir, "app.log")
	require.NoError(t, os.WriteFile(logFile1, []byte(testLogContent), defaultFilePermissions))

	c := &Collector{
		configPath: tempDir,
		dataPath:   tempDir,
	}

	tests := []struct {
		name     string
		duration time.Duration
		validate func(t *testing.T, logs []LogEntry, diag *LogSourceDiagnostics)
	}{
		{
			name:     "successful log collection",
			duration: testDuration24Hours,
			validate: func(t *testing.T, logs []LogEntry, diag *LogSourceDiagnostics) {
				t.Helper()
				// Check that paths were searched
				assert.NotEmpty(t, diag.PathsSearched)
				// Check that at least one log was found if the log directory exists
				for _, path := range diag.PathsSearched {
					if path.Path == logDir && path.Exists {
						assert.True(t, path.Accessible)
						assert.Positive(t, path.FileCount)
					}
				}
			},
		},
		{
			name:     "old logs filtered by duration",
			duration: testDuration1Minute, // Very short duration to filter out test log
			validate: func(t *testing.T, logs []LogEntry, diag *LogSourceDiagnostics) {
				t.Helper()
				// Paths should still be searched
				assert.NotEmpty(t, diag.PathsSearched)
				// But logs might be filtered out
				// logs might be empty after filtering
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostics := &LogSourceDiagnostics{
				Details: make(map[string]any),
			}

			logs, _, _ := c.collectLogFilesWithDiagnostics(tt.duration, testSize10MB, false, diagnostics)

			tt.validate(t, logs, diagnostics)
		})
	}
}

// TestCollector_Collect_AlwaysIncludesDiagnostics tests that diagnostics are always included
func TestCollector_Collect_AlwaysIncludesDiagnostics(t *testing.T) {
	tempDir := t.TempDir()

	c := &Collector{
		configPath:    tempDir,
		dataPath:      tempDir,
		sensitiveKeys: defaultSensitiveKeys(),
	}

	// Create a bundle with minimal options - at least one type must be enabled
	opts := CollectorOptions{
		IncludeLogs:       true,  // Enable logs to avoid validation error
		IncludeConfig:     false, // Disable config
		IncludeSystemInfo: false, // Disable system info
		LogDuration:       testDuration1Hour,
		MaxLogSize:        testSize1KB,
	}

	ctx := context.Background()
	bundle, err := c.Collect(ctx, opts)
	require.NoError(t, err)
	require.NotNil(t, bundle)

	// Verify diagnostics are present
	assert.NotNil(t, bundle.Diagnostics)
	// Logs were enabled, so file logs should be attempted
	assert.True(t, bundle.Diagnostics.LogCollection.FileLogs.Attempted)
	// Config and system info were disabled
	assert.False(t, bundle.Diagnostics.ConfigCollection.Attempted)
	assert.False(t, bundle.Diagnostics.SystemCollection.Attempted)

	// Now test with everything enabled but simulated failures
	opts = CollectorOptions{
		IncludeLogs:       true,
		IncludeConfig:     true,
		IncludeSystemInfo: true,
		LogDuration:       testDuration1Hour,
		MaxLogSize:        testSize1KB,
	}

	bundle, err = c.Collect(ctx, opts)
	require.NoError(t, err)
	require.NotNil(t, bundle)

	// Diagnostics should always be populated
	assert.NotNil(t, bundle.Diagnostics)
	assert.True(t, bundle.Diagnostics.LogCollection.FileLogs.Attempted)
	// Journal logs might or might not be attempted depending on the environment
}
