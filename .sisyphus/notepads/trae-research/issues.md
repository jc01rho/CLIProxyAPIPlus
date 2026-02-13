# Trae AI Research - Issues & Challenges

## Date: 2026-01-28

### Documentation Gaps

**Issue**: Official API documentation is minimal or non-existent
- No public API documentation found at docs.trae.ai
- IDE documentation focuses on usage, not API integration
- Agent documentation is comprehensive but separate product

**Impact**: Third-party integration requires reverse engineering

### Community Project Status

**Issue**: Main community wrapper (trae2api) is archived
- Repository: github.com/linqiu919/trae2api
- Status: Archived August 2025
- Reason: "Project no longer maintained, doesn't support v1.3.0+"

**Impact**: No maintained OpenAI-compatible wrapper available

### Authentication Complexity

**Issue**: Credential extraction is non-trivial
- Requires installing Trae IDE application
- Must extract APP_ID, CLIENT_ID, REFRESH_TOKEN, USER_ID
- No official method for obtaining these credentials
- Device fingerprinting adds complexity

**Impact**: High barrier to entry for API integration

### Version Compatibility

**Issue**: Breaking changes in Trae IDE v1.3.0+
- New models not supported by older wrappers
- API changes not documented
- Community projects unable to keep up

**Impact**: Integration code may break without notice

### Regional Limitations

**Issue**: API endpoints appear region-specific
- Singapore endpoints observed (trae-api-sg.mchost.guru)
- May have latency/availability issues in other regions
- No documentation on regional availability

**Impact**: Potential performance/reliability issues

### Rate Limiting Uncertainty

**Issue**: Rate limits not clearly documented
- Community reports mention "queuing" and "limits"
- No official rate limit documentation found
- Free tier limits unknown

**Impact**: Cannot plan for production usage

### Dual Product Confusion

**Issue**: "Trae AI" refers to two different products
- Trae IDE (desktop app with API)
- Trae Agent (CLI tool)
- Different authentication mechanisms
- Different use cases

**Impact**: Initial research confusion, need to clarify which product is needed
