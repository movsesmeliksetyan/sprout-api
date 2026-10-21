# Adding a statement fixture

The statement corpus is every file under `testdata/statements/` that has an `<name>.expected.json` next to it. `TestStatementCorpus` (`internal/statements/e2e_test.go`, part of `make check`) runs each one through the file stages of an import — detect, extract, resolve (template → heuristics → model), transform, validate — with a fake model that replays recorded answers, and compares the result with the expected file. It also checks the corpus as a whole: at least 20 files from at least 8 countries, at least 90 % `ready` with no mapping from the user, none `failed`. With `-v` it prints each file's status and mapping source.

## 1. Anonymise the statement

Never commit a real statement. Start from a real layout, then replace everything personal:

- names of people (the account holder, payees, payers) with invented ones;
- account numbers, IBANs, sort codes, card numbers with masks (`****1234`, `00000-0000000`) or obviously fake values;
- addresses, phone numbers, emails, references that could identify someone;
- amounts and balances with invented ones — keep running balances consistent if the file has them.

Keep what makes the layout what it is: the headers exactly as the bank writes them, the preamble and footer lines, the date and number formats, the delimiter, the encoding, quoting quirks, merchant-style descriptions.

## 2. Put it in place

Use the directory for its format — `csv/`, `ofx/`, `xlsx/`, `pdf/` — and name it `<country>-<what is special>.<ext>` (`it-uscite-entrate.csv`). A CSV in `csv/` also needs the extraction golden of `TestExtractCSV_Golden`, written by `-update` below.

A PDF must have a text layer (there is no OCR). One way to make one is `makePDF` in `internal/statements/pdf_helper_test.go`, from lines of text, in a throwaway test.

## 3. Write `<name>.expected.json`

Start with what describes the file:

```json
{
  "bank": "Italian, Uscite/Entrate",
  "country": "IT"
}
```

- `country` is ISO 3166-1 alpha-2 (`XX` when the layout is not a country's).
- `month_first: true` reads dates month first when the values cannot tell, as for a US-dollar user.
- `llm` holds the model's answers, by schema name, in the order the pipeline asks:
  - `statement_page_table`: one answer a PDF page, `{"headers": […], "rows": [[…], …]}`, cells copied from the page;
  - `statement_mapping`: the column roles for a layout the heuristics cannot read, `{"columns": [{"index": 0, "role": "date"}, …], "date_format": "DD.MM.YYYY", "amount_sign": "negative_is_expense", "confidence": 0.9}`.

  Without an answer the model refuses, as when it cannot read the file: a PDF is then `failed: unreadable`, and a layout the heuristics cannot read `needs_mapping`.

Then let the runner fill in the results:

```bash
go test ./internal/statements/ -run 'TestStatementCorpus|TestExtractCSV_Golden' -update
```

## 4. Check the results by hand

`-update` writes what the pipeline read, right or wrong. Check every figure against the file before committing:

- `status`: `ready` or `needs_mapping`; `failed` is never accepted;
- `mapping_source`: `heuristic`, `llm` or `native` (OFX);
- `rows` and `error_rows`: every transaction line, and those that could not be read;
- `expense_minor` and `income_minor`: the sums of money out and in, in minor units;
- `first_date` and `last_date`.

If a figure is wrong, fix the reader — the header synonyms in `internal/statements/data/header_synonyms.yaml` are the usual place for a new bank's labels — not the expected file. Run `make check` before committing.
