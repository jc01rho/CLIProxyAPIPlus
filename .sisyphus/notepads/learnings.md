# Learnings

## React / UI Implementation
- **Component Consistency**: When duplicating UI logic (like Tier Badges) from one component (`QuotaCard`) to another (`AuthFilesPage` inline rendering), it's crucial to ensure:
    1.  **Styles**: Corresponding SCSS classes must be present. Copied `.tierBadgeWrapper`, `.tierRefreshBtn`, `.tierRefreshBtnLoading` and keyframes from `QuotaPage.module.scss` to `AuthFilesPage.module.scss`.
    2.  **Logic**: State management for async actions (like `refreshingTier`) needs to be adapted. In `QuotaCard` it was local state for one item; in `AuthFilesPage` it needed to be a map `Record<string, boolean>` to handle multiple items in a list.
    3.  **Event Handling**: `stopPropagation()` is essential for buttons inside clickable cards or complex layouts to prevent unwanted parent events (though `AuthFilesPage` cards aren't fully clickable, it's good practice).

## API Integration
- **Identifier Handling**: When `AuthFileItem` might use `id` or `name` as identifier, robust code should handle both or prioritize one. `providersApi.refreshTier` expects an `authId` (which maps to filename in backend), so using `item.id || item.name` ensures compatibility.

## Cline Model Registry / Dynamic Fetch
- `FetchClineModels`에서 OpenRouter 계열 응답은 `max_completion_tokens`가 모델 루트 또는 `top_provider` 하위에 있을 수 있어 다중 경로 fallback이 필요함.
- 가격 필드(`pricing.prompt`, `pricing.completion`)가 모두 0인 경우를 free 모델로 간주해 description에 `(Free)`를 반영하면 동적 모델 메타데이터 일관성이 높아짐.
