# Kilocode API Research - Issues and Gotchas

## API Limitations
1. **Rate Limiting**: Unknown if Kilocode API has rate limits on /models endpoint
2. **Token Expiration**: Kilocode JWT tokens may expire, need refresh handling
3. **Organization Context**: Some endpoints support organization-specific models

## Free Model Availability Changes
1. **Temporary Free Models**: Some models like "Grok Code Fast 1" were free but became paid
2. **Limited Time Offers**: Models like "Devstral Small 2512" are free for limited time
3. **Provider Changes**: Free tier availability can change based on upstream providers

## Data Routing Concerns
- Issue #2279 raised concerns about data routing through OpenRouter
- Users should be aware that requests go through OpenRouter infrastructure
- Privacy implications for sensitive code

## Model Name Inconsistencies
- Some free models have "(free)" suffix, others don't
- Model IDs may differ from display names
- Need to handle both naming conventions

## Authentication Edge Cases
- JWT token parsing can fail (wrapped in try-catch in codebase)
- Fallback to default base URL if token parsing fails
- Organization-specific tokens may have different base URLs

## Response Format Variations
- Not all models may have complete pricing information
- Some fields might be optional or missing
- Need defensive programming for missing fields
