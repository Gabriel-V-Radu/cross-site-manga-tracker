YAML connectors live in this directory.

Each `.yaml` file can register one API-backed connector.

Minimal schema:

```yaml
key: mysite
name: My Site
enabled: true
base_url: http://localhost:9090
allowed_hosts:
  - mysite.com
health_path: /health

search:
  path: /search
  query_param: q
  limit_param: limit

resolve:
  path: /resolve
  url_param: url

response:
  search_items_path: items
  resolve_item_path: item
  id_field: id
  title_field: title
  url_field: url
  latest_chapter_field: latestChapter
```

Expected search response shape (using default paths):

```json
{
  "items": [
    {"id":"123","title":"Name","url":"https://mysite.com/item/123","latestChapter":12.5}
  ]
}
```

Expected resolve response shape (using default paths):

```json
{
  "item": {"id":"123","title":"Name","url":"https://mysite.com/item/123","latestChapter":12.5}
}
```
