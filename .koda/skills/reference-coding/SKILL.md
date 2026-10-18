---
name: reference-coding
description: Перед написанием кода, реализующего известный паттерн (gRPC-сервер, RabbitMQ-транспорт, pgx-транзакция, gRPC-gateway, сессии/RBAC), найти в локальном индексе XERJ, как его уже решили эталонные open-source проекты, и процитировать находку. Также используется для быстрого поиска по самому проекту без чтения файлов целиком.
---

# Reference coding через XERJ

XERJ — локальный поисковый движок (один бинарник, ES-совместимый REST на `http://localhost:9200`).
Принцип: **сначала достать готовое решение из индексированного эталона, потом писать своё**.
Если поиск по эталону ничего не дал — так и сказать («в корпусе ответа нет»), не выдумывать.

## Нода

Бинарник: `$env:LOCALAPPDATA\Programs\xerj\xerj.exe` (в PATH после перезапуска терминала).
Проверка: `Invoke-RestMethod http://localhost:9200/_cluster/health` отвечает — нода жива.
Если не жива, запустить в фоне (data-dir вне репозиториев, PID-файл для остановки):

```powershell
$p = Start-Process "$env:LOCALAPPDATA\Programs\xerj\xerj.exe" `
  -ArgumentList '--insecure','--data-dir',"$env:LOCALAPPDATA\xerj-data" `
  -WindowStyle Hidden -PassThru
$p.Id | Out-File "$env:LOCALAPPDATA\xerj-node.pid"
```

Останавливать только свою ноду: `Stop-Process (Get-Content "$env:LOCALAPPDATA\xerj-node.pid")`.

## Корпуса

| Индекс | Содержание | Клон |
| --- | --- | --- |
| `ax-*` | сам smart-library (проектный корпус) | рабочее дерево |
| `ww-*` | ThreeDotsLabs/wild-workouts-go-ddd-example — Go DDD-микросервисы, gRPC + RabbitMQ, самая близкая архитектура | `~/Documents/GitHub/xerj-reference/wild-workouts-go-ddd-example` |
| `wm-*` | ThreeDotsLabs/watermill — Go pub/sub поверх RabbitMQ (аналог `pkg/events`) | `.../xerj-reference/watermill` |
| `gw-*` | grpc-ecosystem/grpc-gateway — аннотации `google.api.http`, маршрутизация, Swagger (наша связка в каждом сервисе) | `.../xerj-reference/grpc-gateway` |
| `amqp-*` | rabbitmq/amqp091-go — клиент AMQP: confirms, reconnect, consume (транспорт `pkg/events/amqp`) | `.../xerj-reference/amqp091-go` |

## Как искать

По проекту — одной командой из корня репозитория (ищет по `ax-*`, определения выше ссылок, печатает `file:line`):

```powershell
xerj search "<символ или фраза>"
xerj def "<имя символа>"     # go-to-definition
```

По эталонным корпусам — REST-запрос с defs-first запросом и проекцией полей (обязательно проецировать `_source` и брать `_passage`, иначе ответ — мегабайты):

```powershell
$body = '{"query":{"bool":{"should":[{"multi_match":{"query":"<механизм>","fields":["body","defs"],"type":"most_fields"}},{"match_phrase":{"defs":{"query":"<механизм>","boost":4}}}],"filter":[{"term":{"ax_format":"code"}}]}},"size":5,"_source":["ax_path"],"fields":["_passage"]}'
Invoke-RestMethod "http://127.0.0.1:9200/wm-*/_search" -Method Post -ContentType "application/json" -Body $body
# вместо wm-* — ww-*, gw-*, amqp-*; несколько корпусов разом: wm-*,amqp-* или _search по всем
```

## Протокол перед написанием кода

1. Сформулировать механизм (например «graceful reconnect с backoff в AMQP-консьюмере»).
2. Поискать в подходящем эталонном корпусе; при пустом результате — в остальных (`_search` без индекса = все).
3. Прочитать найденный passage и при необходимости открыть точный файл по `ax_path` в клоне.
4. В коде/PR-описании сослаться: `watermill/internal/close.go:42` (репозиторий/файл:строка).
5. Не копировать код дословно без проверки лицензии: watermill — MIT, wild-workouts — MIT, grpc-gateway — BSD-3, amqp091-go — BSD-2. Заимствовать механизм — да, массовое копирование — только с сохранением copyright.

## Ограничения (важно)

- Эмбеддер по умолчанию **лексический**: искать по точным идентификаторам и терминам (`Confirm`, `SKIP LOCKED`, `PrefetchCount`), а не по вольным перифразам.
- `defs` показывает место **объявления**, а не вызова; графа вызовов отсутствует.
- Индексация: `xerj autoindex <папка> --url http://127.0.0.1:9200 --prefix <px> --state-dir $env:LOCALAPPDATA\xerj-state\<px>` — идемпотентно, повторный запуск докачивает изменения. Код выхода `3` = успех с пропущенными «мусорными» файлами.
- Префиксы заняты: `ax` (проект), `ww`, `wm`, `gw`, `amqp`. Новый эталон — выбрать свободный префикс и добавить строку в таблицу выше и в KODA.md.
