# Trae AI Research - Key Decisions & Observations

## Date: 2026-01-28

### Authentication Approach Decision

**Finding**: Trae AI has TWO separate authentication mechanisms:

1. **For Trae IDE API** (if building integration):
   - Use OAuth 2.0 with refresh tokens
   - Requires extracting credentials from IDE application
   - Not officially documented for third-party use

2. **For Trae Agent CLI** (if using as tool):
   - Use provider-specific API keys
   - Configure via YAML/environment variables
   - Well-documented and officially supported

**Recommendation**: 
- If goal is to USE Trae for coding: Install Trae Agent CLI
- If goal is to INTEGRATE with Trae API: Use community wrappers or reverse-engineer

### API Endpoint Architecture

**Observed Pattern**:
- Regional deployment (Singapore: trae-api-sg.mchost.guru)
- Separate auth domain (api-sg-central.trae.ai)
- ByteDance infrastructure (imagex, tos services)

**Implication**: API may have regional variations and CDN dependencies

### Integration Strategy

**Options Identified**:
1. **Direct Integration**: Reverse-engineer from community projects (complex, unsupported)
2. **Wrapper Usage**: Use trae2api or similar (archived, may break)
3. **Agent Usage**: Use official Trae Agent CLI (recommended, supported)
4. **Provider Direct**: Skip Trae, use LLM providers directly (most reliable)

**Decision Rationale**:
- Official API not publicly documented
- Community wrappers are unmaintained
- Agent CLI is the officially supported integration path
- For production use, direct LLM provider integration is more stable

### Code Example Quality Assessment

**High Quality Examples Found**:
- Official trae-agent repository (well-maintained)
- trae2api wrapper (good reference despite being archived)
- Multiple community integrations showing consistent patterns

**Confidence Level**: HIGH for authentication flow understanding
**Confidence Level**: MEDIUM for long-term API stability

### Security Considerations

**Observations**:
- Credentials include device fingerprinting (x-device-brand, x-device-cpu)
- OAuth refresh tokens used (good security practice)
- No official API key system for third-party developers
- Community projects expose credential extraction methods

**Recommendation**: Treat Trae IDE API as internal/unofficial
