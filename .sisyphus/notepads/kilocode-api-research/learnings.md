# Kilocode API Research - Key Learnings

## API Endpoint Structure
- **Base URL**: `https://api.kilo.ai/api/openrouter/v1`
- **Models Endpoint**: `/models` (full URL: `https://api.kilo.ai/api/openrouter/v1/models`)
- **Chat Endpoint**: `/chat/completions`

## Authentication
- Uses Bearer token authentication with Kilocode JWT tokens
- Token format: `Authorization: Bearer <kilocode_token>`
- Tokens contain base URL information in JWT payload

## API Response Format
Based on OpenRouter API standard, the `/models` endpoint returns:

```json
{
  "data": [
    {
      "id": "model-id",
      "name": "Model Name", 
      "created": 1692901234,
      "pricing": {
        "prompt": "0.00003",
        "completion": "0.00006", 
        "request": "0",
        "image": "0"
      },
      "context_length": 8192,
      "architecture": {
        "modality": "text->text",
        "input_modalities": ["text"],
        "output_modalities": ["text"],
        "tokenizer": "GPT",
        "instruct_type": "chatml"
      },
      "top_provider": {
        "is_moderated": true,
        "context_length": 8192
      }
    }
  ]
}
```

## Free Model Identification
Free models are identified by pricing fields with "0" values:
- `pricing.prompt = "0"`
- `pricing.completion = "0"` 
- `pricing.request = "0"`

## Current Free Models Available (2026)
Based on Kilocode documentation and model leaderboard:

### Completely Free Models:
1. **Grok Code Fast 1** (xAI) - Was free, now going paid
2. **Mistral: Devstral Small 2512** - Free in Kilo Code for limited time
3. **Arcee AI: Trinity Large Preview** - Free tier available
4. **Xiaomi: MiMo-V2-Flash** - Free open-source model

### OpenRouter Free Tier Models (via Kilocode):
1. **Qwen3 Coder (free)** - Optimized for agentic coding tasks
2. **Z.AI: GLM 4.5 Air (free)** - Lightweight variant for agent applications  
3. **DeepSeek: R1 0528 (free)** - Performance on par with OpenAI o1
4. **MoonshotAI: Kimi K2 (free)** - Optimized for agentic capabilities

## Implementation Notes
- Kilocode uses OpenRouter-compatible API format
- Models are fetched using `getOpenRouterModels()` function
- Free models have `$0.00/1M` pricing in the leaderboard
- Some models marked as "(free)" in their names
