package keeperexport_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/keeperexport"
)

const fixtureDir = "testdata/v1"

const (
	fixtureInstanceID   = "0198aa10-4d88-7a20-8f4e-8c8de4a9cb11"
	fixtureCredentialID = "0198aa10-4d88-7a20-8f4e-8c8de4a9cb12"
	fixtureStreamID     = "0198aa11-1055-7f12-8a00-e843d1e17522"
	fixtureFingerprint  = "akf1_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func requireProtocolError(t *testing.T, err *keeperexport.Error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected protocol error %q, got nil", wantCode)
	}
	if err.Code != wantCode {
		t.Fatalf("error code = %q, want %q (message %q)", err.Code, wantCode, err.Message)
	}
}

func requireNoProtocolError(t *testing.T, err *keeperexport.Error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected protocol error %q: %s", err.Code, err.Message)
	}
}

// TestKeeperExportProtocolFixtures decodes every shared golden fixture with the
// strict keeper-export/v1 decoders and asserts the frozen accept/reject results.
func TestKeeperExportProtocolFixtures(t *testing.T) {
	t.Run("Manifest", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(fixtureDir, "manifest.sha256"))
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}
		listed := map[string]string{}
		for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
			hash, name, ok := strings.Cut(line, "  ")
			if !ok || hash == "" || name == "" {
				t.Fatalf("malformed manifest line %q", line)
			}
			if _, dup := listed[name]; dup {
				t.Fatalf("manifest lists %s twice", name)
			}
			listed[name] = hash
		}
		entries, err := os.ReadDir(fixtureDir)
		if err != nil {
			t.Fatalf("read fixture dir: %v", err)
		}
		var onDisk []string
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".json") {
				continue
			}
			onDisk = append(onDisk, name)
			want, ok := listed[name]
			if !ok {
				t.Fatalf("fixture %s missing from manifest", name)
			}
			sum := sha256.Sum256(readFixture(t, name))
			if got := hex.EncodeToString(sum[:]); got != want {
				t.Fatalf("manifest hash mismatch for %s: manifest %s, actual %s", name, want, got)
			}
			data := readFixture(t, name)
			if !strings.HasSuffix(string(data), "\n") || strings.HasSuffix(string(data), "\n\n") {
				t.Fatalf("fixture %s must end with exactly one LF", name)
			}
		}
		sort.Strings(onDisk)
		var listedNames []string
		for name := range listed {
			listedNames = append(listedNames, name)
		}
		sort.Strings(listedNames)
		if strings.Join(onDisk, ",") != strings.Join(listedNames, ",") {
			t.Fatalf("manifest entries %v do not match on-disk fixtures %v", listedNames, onDisk)
		}
	})

	t.Run("Valid", func(t *testing.T) {
		t.Run("fingerprint-vectors", func(t *testing.T) {
			vectors, err := keeperexport.DecodeFingerprintVectors(readFixture(t, "fingerprint-vectors.valid.json"))
			requireNoProtocolError(t, err)
			secret, decErr := hex.DecodeString(vectors.FingerprintSecretHex)
			if decErr != nil || len(secret) != 32 {
				t.Fatalf("fingerprintSecretHex must decode to 32 bytes: %v", decErr)
			}
			if len(vectors.Vectors) == 0 {
				t.Fatal("fixture must contain at least one vector")
			}
			for i, vector := range vectors.Vectors {
				got := keeperexport.APIKeyFingerprint(secret, []byte(vector.RawKeyUtf8))
				switch {
				case vector.Fingerprint == nil && got != nil:
					t.Fatalf("vector %d: expected nil fingerprint, got %q", i, *got)
				case vector.Fingerprint != nil && got == nil:
					t.Fatalf("vector %d: expected %q, got nil", i, *vector.Fingerprint)
				case vector.Fingerprint != nil && got != nil && *got != *vector.Fingerprint:
					t.Fatalf("vector %d: fingerprint = %q, want %q", i, *got, *vector.Fingerprint)
				}
			}
		})

		t.Run("identity-response", func(t *testing.T) {
			resp, err := keeperexport.DecodeIdentityResponse(readFixture(t, "identity-response.valid.json"))
			requireNoProtocolError(t, err)
			if resp.Instance.InstanceID != fixtureInstanceID || resp.Instance.DisplayName == "" {
				t.Fatalf("instance = %+v", resp.Instance)
			}
			if resp.Credential.CredentialID != fixtureCredentialID {
				t.Fatalf("credentialId = %q", resp.Credential.CredentialID)
			}
			wantScopes := []string{"usage:push", "metadata:push", "identity:test"}
			if strings.Join(resp.Credential.Scopes, ",") != strings.Join(wantScopes, ",") {
				t.Fatalf("scopes = %v, want %v", resp.Credential.Scopes, wantScopes)
			}
		})

		t.Run("instance-registration", func(t *testing.T) {
			resp, err := keeperexport.DecodeInstanceRegistration(readFixture(t, "instance-registration.valid.json"))
			requireNoProtocolError(t, err)
			if resp.Credential.Token == nil || *resp.Credential.Token != "fixture_ingest_token_not_secret" {
				t.Fatal("registration must disclose the one-time fake fixture token")
			}
			if !resp.Instance.Enabled {
				t.Fatal("instance must be enabled")
			}
		})

		t.Run("usage-batch", func(t *testing.T) {
			batch, err := keeperexport.DecodeUsageBatch(readFixture(t, "usage-batch.valid.json"))
			requireNoProtocolError(t, err)
			if batch.StreamID != fixtureStreamID {
				t.Fatalf("streamId = %q", batch.StreamID)
			}
			if len(batch.Events) != 2 || batch.Events[0].Sequence != 1 || batch.Events[1].Sequence != 2 {
				t.Fatalf("events = %+v", batch.Events)
			}
			// Duplicate request_id across both events proves correlation is not dedup.
			first := batch.Events[0].Payload
			second := batch.Events[1].Payload
			if first.RequestID == "" || first.RequestID != second.RequestID {
				t.Fatalf("request_id correlation: %q vs %q", first.RequestID, second.RequestID)
			}
			if first.APIKeyFingerprint == nil || *first.APIKeyFingerprint != fixtureFingerprint {
				t.Fatalf("api_key_fingerprint = %v", first.APIKeyFingerprint)
			}
			if first.Fail.Code != nil {
				t.Fatalf("fail.code = %v, want nil", *first.Fail.Code)
			}
			if len(first.ResponseHeaders) != 3 {
				t.Fatalf("response_headers = %v, want 3 allowlisted keys", first.ResponseHeaders)
			}
			if len(batch.Events[0].RawPayload) == 0 || len(batch.Events[0].RawPayload) > keeperexport.MaxPayloadBytes {
				t.Fatalf("raw payload length = %d", len(batch.Events[0].RawPayload))
			}
			digest := sha256.Sum256(batch.Events[0].RawPayload)
			if digest == ([32]byte{}) {
				t.Fatal("payload digest must be computable from raw bytes")
			}
		})

		t.Run("usage-ack", func(t *testing.T) {
			ack, err := keeperexport.DecodeUsageAck(readFixture(t, "usage-ack.valid.json"))
			requireNoProtocolError(t, err)
			if ack.AcknowledgedThrough != 2 || ack.NextExpectedSequence != 3 || ack.AcceptedCount != 2 || ack.ReplayedCount != 0 {
				t.Fatalf("ack = %+v", ack)
			}
			requireNoProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureStreamID, 0, 3, 2))
		})

		t.Run("metadata-auth-files", func(t *testing.T) {
			snap, err := keeperexport.DecodeMetadataSnapshot(readFixture(t, "metadata-auth-files.valid.json"), keeperexport.CategoryAuthFiles)
			requireNoProtocolError(t, err)
			if snap.Revision != 1 || len(snap.AuthFiles) != 2 {
				t.Fatalf("snapshot = %+v", snap)
			}
			if snap.AuthFiles[0].AuthIndex == snap.AuthFiles[1].AuthIndex {
				t.Fatal("fixture must use distinct auth indexes")
			}
		})

		t.Run("metadata-api-keys", func(t *testing.T) {
			snap, err := keeperexport.DecodeMetadataSnapshot(readFixture(t, "metadata-api-keys.valid.json"), keeperexport.CategoryAPIKeys)
			requireNoProtocolError(t, err)
			if len(snap.APIKeys) != 2 || snap.APIKeys[0].Fingerprint != fixtureFingerprint {
				t.Fatalf("snapshot = %+v", snap)
			}
		})

		t.Run("metadata-provider-identities", func(t *testing.T) {
			snap, err := keeperexport.DecodeMetadataSnapshot(readFixture(t, "metadata-provider-identities.valid.json"), keeperexport.CategoryProviderIdentities)
			requireNoProtocolError(t, err)
			if len(snap.ProviderIdentities) != 2 {
				t.Fatalf("snapshot = %+v", snap)
			}
			a, b := snap.ProviderIdentities[0], snap.ProviderIdentities[1]
			if a.DisplayName != b.DisplayName || a.AuthIndex == b.AuthIndex {
				t.Fatal("fixture must collide display names but keep distinct auth indexes")
			}
		})

		t.Run("metadata-empty-complete", func(t *testing.T) {
			snap, err := keeperexport.DecodeMetadataSnapshot(readFixture(t, "metadata-empty-complete.valid.json"), keeperexport.CategoryAuthFiles)
			requireNoProtocolError(t, err)
			if snap.Revision != 2 || len(snap.AuthFiles) != 0 {
				t.Fatalf("snapshot = %+v", snap)
			}
		})

		t.Run("settings-response", func(t *testing.T) {
			resp, err := keeperexport.DecodeSettingsResponse(readFixture(t, "settings-response.valid.json"))
			requireNoProtocolError(t, err)
			if !resp.Settings.Keeper.TokenConfigured {
				t.Fatal("tokenConfigured must be true in the response fixture")
			}
			if resp.Settings.Keeper.URL != "https://keeper.example.com" || resp.Settings.Keeper.TokenEnv != "CPA_KEEPER_INGEST_TOKEN" {
				t.Fatalf("keeper = %+v", resp.Settings.Keeper)
			}
			if resp.Settings.Delivery.MaxBatchEvents != 500 || resp.Settings.Delivery.MaxBatchBytes != 1048576 {
				t.Fatalf("delivery = %+v", resp.Settings.Delivery)
			}
		})

		t.Run("connection-test-response", func(t *testing.T) {
			resp, err := keeperexport.DecodeConnectionTestResponse(readFixture(t, "connection-test-response.valid.json"))
			requireNoProtocolError(t, err)
			if !resp.OK || resp.Instance.InstanceID != fixtureInstanceID || resp.LatencyMs != 42 {
				t.Fatalf("connection test = %+v", resp)
			}
		})

		t.Run("status-connected", func(t *testing.T) {
			status, err := keeperexport.DecodeStatusResponse(readFixture(t, "status-connected.valid.json"))
			requireNoProtocolError(t, err)
			if status.State != keeperexport.StateConnected || status.LastError != nil {
				t.Fatalf("status = %+v", status)
			}
			if status.AcknowledgedThrough == nil || *status.AcknowledgedThrough != 12 || status.NextSequence == nil || *status.NextSequence != 13 {
				t.Fatalf("watermarks = %+v", status)
			}
			for _, category := range []string{"auth_files", "api_keys", "provider_identities"} {
				if _, ok := status.MetadataRevisions[category]; !ok {
					t.Fatalf("metadataRevisions missing %q", category)
				}
			}
		})

		t.Run("status-retrying", func(t *testing.T) {
			status, err := keeperexport.DecodeStatusResponse(readFixture(t, "status-retrying.valid.json"))
			requireNoProtocolError(t, err)
			if status.State != keeperexport.StateRetrying || status.BacklogEvents == 0 || status.BacklogBytes == 0 {
				t.Fatalf("status = %+v", status)
			}
			if status.LastError == nil || status.LastError.Code != "keeper_timeout" || !status.LastError.Retryable {
				t.Fatalf("lastError = %+v", status.LastError)
			}
		})

		t.Run("error-conflicting-replay", func(t *testing.T) {
			envelope, err := keeperexport.DecodeErrorEnvelope(readFixture(t, "error-conflicting-replay.valid.json"))
			requireNoProtocolError(t, err)
			if envelope.Error.Code != "conflicting_replay" || envelope.Error.Retryable {
				t.Fatalf("error = %+v", envelope.Error)
			}
			if got := keeperexport.HTTPStatusForCode(envelope.Error.Code); got != 409 {
				t.Fatalf("HTTPStatusForCode(conflicting_replay) = %d, want 409", got)
			}
		})
	})

	t.Run("Invalid", func(t *testing.T) {
		cases := []struct {
			name     string
			expected string
			decode   func(data []byte) *keeperexport.Error
		}{
			{"invalid-version.json", "unsupported_protocol_version", decodeUsageBatchErr},
			{"invalid-body-instance-id.json", "body_instance_forbidden", decodeUsageBatchErr},
			{"invalid-usage-gap-order.json", "invalid_sequence_order", decodeUsageBatchErr},
			{"invalid-usage-duplicate-sequence.json", "invalid_sequence_order", decodeUsageBatchErr},
			{"invalid-usage-raw-api-key.json", "unknown_field", decodeUsageBatchErr},
			{"invalid-usage-provider-secret-header.json", "invalid_field", decodeUsageBatchErr},
			{"invalid-usage-raw-failure-body.json", "unknown_field", decodeUsageBatchErr},
			{"invalid-usage-oversized-batch.json", "batch_limit_exceeded", decodeUsageBatchErr},
			{"invalid-usage-oversized-payload.json", "batch_limit_exceeded", decodeUsageBatchErr},
			{"invalid-metadata-incomplete.json", "incomplete_snapshot", decodeAuthFilesErr},
			{"invalid-metadata-duplicate-identity.json", "duplicate_metadata_identity", decodeAuthFilesErr},
			{"invalid-settings-token-value.json", "unknown_field", decodeSettingsPutErr},
			{"invalid-settings-token-configured-write.json", "unknown_field", decodeSettingsPutErr},
			{"invalid-settings-http-url.json", "invalid_settings", decodeSettingsPutErr},
			{"invalid-status-token-leak.json", "unknown_field", decodeStatusErr},
			{"invalid-unknown-field.json", "unknown_field", decodeUsageBatchErr},
			{"invalid-duplicate-json-key.json", "invalid_json", decodeUsageBatchErr},
			{"invalid-credential-scope.json", "invalid_field", decodeCreateInstanceErr},
		}
		for _, tc := range cases {
			t.Run(strings.TrimSuffix(tc.name, ".json"), func(t *testing.T) {
				requireProtocolError(t, tc.decode(readFixture(t, tc.name)), tc.expected)
			})
		}

		t.Run("invalid-metadata-stale-revision", func(t *testing.T) {
			data := readFixture(t, "invalid-metadata-stale-revision.json")
			snap, err := keeperexport.DecodeMetadataSnapshot(data, keeperexport.CategoryAuthFiles)
			requireNoProtocolError(t, err)
			// Fixture state: current revision is 2; submitting revision 1 is stale.
			revErr := keeperexport.CheckMetadataRevision(2, nil, snap.Revision, data)
			requireProtocolError(t, revErr, "stale_revision")
		})

		t.Run("invalid-metadata-conflicting-revision", func(t *testing.T) {
			applied := readFixture(t, "metadata-auth-files.valid.json")
			appliedDigest := sha256.Sum256(applied)
			data := readFixture(t, "invalid-metadata-conflicting-revision.json")
			snap, err := keeperexport.DecodeMetadataSnapshot(data, keeperexport.CategoryAuthFiles)
			requireNoProtocolError(t, err)
			// Fixture state: a different revision-1 snapshot was already applied.
			revErr := keeperexport.CheckMetadataRevision(1, appliedDigest[:], snap.Revision, data)
			requireProtocolError(t, revErr, "conflicting_revision")
		})

		t.Run("metadata-equal-revision-exact-replay-is-idempotent", func(t *testing.T) {
			applied := readFixture(t, "metadata-auth-files.valid.json")
			appliedDigest := sha256.Sum256(applied)
			snap, err := keeperexport.DecodeMetadataSnapshot(applied, keeperexport.CategoryAuthFiles)
			requireNoProtocolError(t, err)
			requireNoProtocolError(t, keeperexport.CheckMetadataRevision(1, appliedDigest[:], snap.Revision, applied))
		})

		t.Run("metadata-newer-revision-applies", func(t *testing.T) {
			applied := readFixture(t, "metadata-auth-files.valid.json")
			appliedDigest := sha256.Sum256(applied)
			data := readFixture(t, "metadata-empty-complete.valid.json")
			snap, err := keeperexport.DecodeMetadataSnapshot(data, keeperexport.CategoryAuthFiles)
			requireNoProtocolError(t, err)
			requireNoProtocolError(t, keeperexport.CheckMetadataRevision(1, appliedDigest[:], snap.Revision, data))
		})
	})

	t.Run("StatefulPairs", func(t *testing.T) {
		t.Run("replay-exact", func(t *testing.T) {
			_, err := keeperexport.DecodeUsageBatch(readFixture(t, "replay-exact.request.json"))
			requireNoProtocolError(t, err)
			ack, ackErr := keeperexport.DecodeUsageAck(readFixture(t, "replay-exact.expected.json"))
			requireNoProtocolError(t, ackErr)
			if ack.AcceptedCount != 0 || ack.ReplayedCount != 2 || ack.AcknowledgedThrough != 2 || ack.NextExpectedSequence != 3 {
				t.Fatalf("ack = %+v, want accepted 0 replayed 2 ack 2 next 3", ack)
			}
			requireNoProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureStreamID, 2, 3, 2))
		})

		t.Run("replay-conflict", func(t *testing.T) {
			// The conflicting request is still a wire-valid batch; only the
			// server-side delivery ledger detects the digest mismatch.
			_, err := keeperexport.DecodeUsageBatch(readFixture(t, "replay-conflict.request.json"))
			requireNoProtocolError(t, err)
			envelope, envErr := keeperexport.DecodeErrorEnvelope(readFixture(t, "replay-conflict.expected.json"))
			requireNoProtocolError(t, envErr)
			if envelope.Error.Code != "conflicting_replay" {
				t.Fatalf("error = %+v", envelope.Error)
			}
		})

		t.Run("gap-before", func(t *testing.T) {
			batch, err := keeperexport.DecodeUsageBatch(readFixture(t, "gap-before.request.json"))
			requireNoProtocolError(t, err)
			if len(batch.Events) != 1 || batch.Events[0].Sequence != 12 {
				t.Fatalf("events = %+v", batch.Events)
			}
			ack, ackErr := keeperexport.DecodeUsageAck(readFixture(t, "gap-before.expected.json"))
			requireNoProtocolError(t, ackErr)
			if ack.AcknowledgedThrough != 10 || ack.NextExpectedSequence != 11 {
				t.Fatalf("ack = %+v, want ack 10 next 11 (gap must not advance ACK)", ack)
			}
		})

		t.Run("gap-fill", func(t *testing.T) {
			batch, err := keeperexport.DecodeUsageBatch(readFixture(t, "gap-fill.request.json"))
			requireNoProtocolError(t, err)
			if len(batch.Events) != 1 || batch.Events[0].Sequence != 11 {
				t.Fatalf("events = %+v", batch.Events)
			}
			ack, ackErr := keeperexport.DecodeUsageAck(readFixture(t, "gap-fill.expected.json"))
			requireNoProtocolError(t, ackErr)
			if ack.AcknowledgedThrough != 12 || ack.NextExpectedSequence != 13 {
				t.Fatalf("ack = %+v, want ack 12 next 13", ack)
			}
			requireNoProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureStreamID, 10, 13, 1))
		})

		t.Run("metadata-empty-complete-expected", func(t *testing.T) {
			resp, err := keeperexport.DecodeMetadataApplyResponse(readFixture(t, "metadata-empty-complete.expected.json"))
			requireNoProtocolError(t, err)
			if !resp.Applied || resp.ItemCount != 0 || resp.Category != keeperexport.CategoryAuthFiles || resp.Revision != 2 {
				t.Fatalf("response = %+v", resp)
			}
		})

		t.Run("metadata-incomplete-expected", func(t *testing.T) {
			envelope, err := keeperexport.DecodeErrorEnvelope(readFixture(t, "metadata-incomplete.expected.json"))
			requireNoProtocolError(t, err)
			if envelope.Error.Code != "incomplete_snapshot" {
				t.Fatalf("error = %+v", envelope.Error)
			}
		})
	})

	t.Run("ACKInvariantViolations", func(t *testing.T) {
		mutateAck := func(t *testing.T, fn func(m map[string]interface{})) []byte {
			t.Helper()
			var m map[string]interface{}
			if err := json.Unmarshal(readFixture(t, "usage-ack.valid.json"), &m); err != nil {
				t.Fatal(err)
			}
			fn(m)
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}

		t.Run("wrong-version", func(t *testing.T) {
			data := mutateAck(t, func(m map[string]interface{}) { m["protocolVersion"] = "keeper-export/v2" })
			_, err := keeperexport.DecodeUsageAck(data)
			requireProtocolError(t, err, "keeper_invalid_response")
		})
		t.Run("unknown-field", func(t *testing.T) {
			data := mutateAck(t, func(m map[string]interface{}) { m["token"] = "leak" })
			_, err := keeperexport.DecodeUsageAck(data)
			requireProtocolError(t, err, "keeper_invalid_response")
		})
		t.Run("wrong-stream", func(t *testing.T) {
			ack, err := keeperexport.DecodeUsageAck(readFixture(t, "usage-ack.valid.json"))
			requireNoProtocolError(t, err)
			requireProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureInstanceID, 0, 3, 2), "keeper_invalid_response")
		})
		t.Run("noncontiguous-next-expected", func(t *testing.T) {
			data := mutateAck(t, func(m map[string]interface{}) { m["nextExpectedSequence"] = float64(5) })
			ack, err := keeperexport.DecodeUsageAck(data)
			requireNoProtocolError(t, err)
			requireProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureStreamID, 0, 3, 2), "keeper_invalid_response")
		})
		t.Run("ack-regression", func(t *testing.T) {
			ack, err := keeperexport.DecodeUsageAck(readFixture(t, "usage-ack.valid.json"))
			requireNoProtocolError(t, err)
			requireProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureStreamID, 3, 4, 2), "keeper_invalid_response")
		})
		t.Run("ack-not-below-next-sequence", func(t *testing.T) {
			ack, err := keeperexport.DecodeUsageAck(readFixture(t, "usage-ack.valid.json"))
			requireNoProtocolError(t, err)
			requireProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureStreamID, 0, 2, 2), "keeper_invalid_response")
		})
		t.Run("count-mismatch", func(t *testing.T) {
			ack, err := keeperexport.DecodeUsageAck(readFixture(t, "usage-ack.valid.json"))
			requireNoProtocolError(t, err)
			requireProtocolError(t, keeperexport.ValidateUsageAck(ack, fixtureStreamID, 0, 3, 5), "keeper_invalid_response")
		})
	})
}

func decodeUsageBatchErr(data []byte) *keeperexport.Error {
	_, err := keeperexport.DecodeUsageBatch(data)
	return err
}

func decodeAuthFilesErr(data []byte) *keeperexport.Error {
	_, err := keeperexport.DecodeMetadataSnapshot(data, keeperexport.CategoryAuthFiles)
	return err
}

func decodeSettingsPutErr(data []byte) *keeperexport.Error {
	_, err := keeperexport.DecodeSettingsPutRequest(data)
	return err
}

func decodeStatusErr(data []byte) *keeperexport.Error {
	_, err := keeperexport.DecodeStatusResponse(data)
	return err
}

func decodeCreateInstanceErr(data []byte) *keeperexport.Error {
	_, err := keeperexport.DecodeCreateInstanceRequest(data)
	return err
}
