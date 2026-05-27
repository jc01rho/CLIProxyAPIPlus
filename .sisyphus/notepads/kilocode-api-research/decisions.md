# Kilocode API Research - Key Decisions

## Free Model Detection Strategy
Based on research, free models can be identified by:

1. **Primary Method**: Check `pricing` object fields
   - `pricing.prompt === "0"`
   - `pricing.completion === "0"`
   - Both conditions should be true for a model to be considered free

2. **Secondary Method**: Model name contains "(free)" suffix
   - Some models explicitly marked as "(free)" in their names
   - Examples: "Qwen3 Coder (free)", "GLM 4.5 Air (free)"

3. **Fallback Method**: Check if all pricing fields are "0"
   - `pricing.request === "0"`
   - `pricing.image === "0"`

## API Integration Approach
- Use existing `getOpenRouterModels()` function pattern
- Endpoint: `https://api.kilo.ai/api/openrouter/v1/models`
- Authentication: Bearer token with Kilocode JWT
- Response format follows OpenRouter standard

## Model Registration Logic
Filter models where:
```javascript
model.pricing && 
model.pricing.prompt === "0" && 
model.pricing.completion === "0"
```

## Error Handling
- Handle cases where pricing object might be missing
- Graceful fallback if API is unavailable
- Log warnings for unexpected response formats
