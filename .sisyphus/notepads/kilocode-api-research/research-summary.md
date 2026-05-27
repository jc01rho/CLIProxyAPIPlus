# Kilocode API Research Summary

## 1. API Endpoint Response Format

### `/api/openrouter/v1/models` Endpoint
- **Full URL**: `https://api.kilo.ai/api/openrouter/v1/models`
- **Authentication**: Bearer token with Kilocode JWT
- **Response Format**: OpenRouter-compatible JSON structure

### Exact JSON Structure:
```json
{
  "data": [
    {
      "id": "model-provider/model-name",
      "canonical_slug": "model-provider-model-name", 
      "name": "Human Readable Model Name",
      "created": 1692901234,
      "pricing": {
        "prompt": "0.00003",      // Cost per input token (USD)
        "completion": "0.00006",  // Cost per output token (USD) 
        "request": "0",           // Cost per request (USD)
        "image": "0",             // Cost per image (USD)
        "web_search": "0",        // Cost per web search (USD)
        "internal_reasoning": "0", // Cost per reasoning token (USD)
        "input_cache_read": "0",  // Cost per cached input token (USD)
        "input_cache_write": "0"  // Cost per cache write token (USD)
      },
      "context_length": 8192,
      "architecture": {
        "modality": "text->text",
        "input_modalities": ["text", "image"],
        "output_modalities": ["text"],
        "tokenizer": "GPT",
        "instruct_type": "chatml"
      },
      "top_provider": {
        "is_moderated": true,
        "context_length": 8192,
        "max_completion_tokens": 4096
      },
      "per_request_limits": null,
      "supported_parameters": ["temperature", "top_p", "max_tokens"]
    }
  ]
}
```

## 2. Free Model Identification

### Primary Method: Pricing Fields
A model is considered **FREE** when:
```javascript
model.pricing && 
model.pricing.prompt === "0" && 
model.pricing.completion === "0"
```

### Secondary Indicators:
1. **Name suffix**: Models with "(free)" in the name
2. **All pricing zeros**: All pricing fields equal "0"
3. **Request cost**: `pricing.request === "0"`

## 3. Current Free Models (January 2026)

### Confirmed Free Models Available:
1. **Mistral: Devstral 2 2512 (free)**
   - ID: `mistralai/devstral-2-2512-free`
   - Pricing: `$0.00/1M`
   - Status: Free for limited time in Kilo Code

2. **Arcee AI: Trinity Large Preview (free)**
   - ID: `arcee-ai/trinity-large-preview`
   - Pricing: `$0.00/1M`
   - Status: Free tier available

3. **Xiaomi: MiMo-V2-Flash (free)**
   - ID: `xiaomi/mimo-v2-flash`
   - Pricing: `$0.00/1M`
   - Status: Free open-source model

### OpenRouter Free Tier Models (via Kilocode):
4. **Qwen3 Coder (free)**
   - Optimized for agentic coding tasks
   - Function calling and tool use capabilities

5. **Z.AI: GLM 4.5 Air (free)**
   - Lightweight variant for agent applications
   - Purpose-built for agentic workflows

6. **DeepSeek: R1 0528 (free)**
   - Performance comparable to OpenAI o1
   - Open-sourced with reasoning tokens

7. **MoonshotAI: Kimi K2 (free)**
   - Advanced tool use and reasoning
   - Code synthesis capabilities

### Recently Changed to Paid:
- **Grok Code Fast 1** (xAI) - Was free until January 2026, now paid

## 4. Implementation Recommendations

### Model Registration Logic:
```javascript
function isModelFree(model) {
  // Primary check: pricing fields
  if (model.pricing) {
    const promptFree = model.pricing.prompt === "0";
    const completionFree = model.pricing.completion === "0";
    return promptFree && completionFree;
  }
  
  // Fallback: check name for "(free)" suffix
  return model.name && model.name.includes("(free)");
}

function filterFreeModels(models) {
  return models.filter(isModelFree);
}
```

### Error Handling:
```javascript
function fetchKilocodeFreeModels(apiKey) {
  try {
    const response = await fetch('https://api.kilo.ai/api/openrouter/v1/models', {
      headers: {
        'Authorization': `Bearer ${apiKey}`,
        'Content-Type': 'application/json'
      }
    });
    
    if (!response.ok) {
      throw new Error(`API request failed: ${response.status}`);
    }
    
    const data = await response.json();
    return filterFreeModels(data.data || []);
  } catch (error) {
    console.warn('Failed to fetch Kilocode models:', error);
    return []; // Return empty array on failure
  }
}
```

## 5. Key Findings

### API Compatibility:
- Kilocode API follows OpenRouter standard exactly
- Same response format as OpenRouter `/api/v1/models`
- Compatible with existing OpenRouter integration code

### Authentication:
- Uses Kilocode JWT tokens as Bearer authentication
- Tokens contain base URL information in payload
- Organization-specific endpoints available

### Free Model Strategy:
- Mix of permanently free and temporarily free models
- Some models free only through Kilocode partnership
- Pricing can change (e.g., Grok Code Fast 1)

### Data Routing:
- Requests route through OpenRouter infrastructure
- Privacy considerations for sensitive code
- Users should be aware of data flow

## 6. Next Steps for Implementation

1. **Use existing `getOpenRouterModels()` function**
2. **Add free model filtering logic**
3. **Handle authentication with Kilocode tokens**
4. **Implement error handling for API failures**
5. **Add logging for model availability changes**
6. **Consider caching to reduce API calls**

This research provides the complete foundation needed to implement Kilocode free model detection and registration in CLIProxyAPIPlus.
