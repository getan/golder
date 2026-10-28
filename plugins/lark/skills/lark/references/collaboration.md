# Documents and conversations

1. Search by topic or resolve an exact URL/ID. Disambiguate same-name documents
   and chats. `doc_search` query is at most 30 Unicode characters; use filters.
2. `doc_read` supports `scope=outline|section|keyword|range|full`. Prefer the
   smallest relevant excerpt; do not read all search hits in full.
3. `doc_write` operations: create, append, replace_text, replace_block. Supply
   Markdown content inline. For a replacement, read the actual text/block first;
   append is not a whole-document overwrite.
4. Chat/message tools use `chat_id` or `user_id`. A name is not a recipient ID.
   Bound history/search by time and page size; return useful quotations and links.
5. `message_send` accepts exactly one of text/markdown and one chat/user target.
   Show recipient and the complete body for confirmation. A submission receipt
   does not mean the recipient read the message.

Example: `doc_read` arguments:

```json
{"options":{"doc":"https://tenant.feishu.cn/docx/actual-token","scope":"keyword","keyword":"排期"}}
```
