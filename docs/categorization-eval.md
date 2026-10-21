# Categorisation evaluation

`make eval-llm` on 2026-10-21 with `qwen/qwen3.8-27b` (groq). The labelled set is `testdata/categorize/labelled.jsonl`: 555 rows, synthetic and anonymised, written alongside the seed data, so it measures the pipeline's wiring and the seeds' reach more than accuracy on real statements.

Targets (PRD §5.5): deterministic coverage ≥ 60 % and precision ≥ 95 %; full pipeline top-1 accuracy ≥ 90 %.

### Deterministic layers (1–4)

515 expense rows: coverage 89.9 %, precision on covered rows 99.1 %, top-1 accuracy 89.1 %.

By source: dictionary 290, keyword 156, mcc 17, none 52.

| Labelled | Rows | Right | Wrong | None |
|---|---:|---:|---:|---:|
| education | 12 | 12 | 0 | 0 |
| family | 7 | 7 | 0 | 0 |
| food | 163 | 147 | 0 | 16 |
| gifts | 9 | 9 | 0 | 0 |
| health | 35 | 35 | 0 | 0 |
| housing | 18 | 18 | 0 | 0 |
| leisure | 41 | 34 | 2 | 5 |
| pets | 9 | 9 | 0 | 0 |
| selfcare | 33 | 28 | 0 | 5 |
| shopping | 49 | 37 | 2 | 10 |
| transport | 59 | 55 | 0 | 4 |
| travel | 35 | 29 | 0 | 6 |
| utilities | 31 | 31 | 0 | 0 |
| work | 14 | 8 | 0 | 6 |

Wrong answers:

- "POS PURCHASE THE WORKS #5261 SEATTLE WA": family by mcc, want shopping
- "KARTENZAHLUNG THE WORKS BERLIN DE": family by mcc, want shopping
- "POS PURCHASE WARHAMMER #8931 BOSTON WA": family by mcc, want leisure
- "KARTENZAHLUNG WARHAMMER HAMBURG DE": family by mcc, want leisure

### Full pipeline (1–5)

515 expense rows: coverage 96.7 %, precision on covered rows 99.2 %, top-1 accuracy 95.9 %.

By source: dictionary 290, keyword 156, llm 35, mcc 17, none 17.

| Labelled | Rows | Right | Wrong | None |
|---|---:|---:|---:|---:|
| education | 12 | 12 | 0 | 0 |
| family | 7 | 7 | 0 | 0 |
| food | 163 | 152 | 0 | 11 |
| gifts | 9 | 9 | 0 | 0 |
| health | 35 | 35 | 0 | 0 |
| housing | 18 | 18 | 0 | 0 |
| leisure | 41 | 37 | 2 | 2 |
| pets | 9 | 9 | 0 | 0 |
| selfcare | 33 | 31 | 0 | 2 |
| shopping | 49 | 45 | 2 | 2 |
| transport | 59 | 59 | 0 | 0 |
| travel | 35 | 35 | 0 | 0 |
| utilities | 31 | 31 | 0 | 0 |
| work | 14 | 14 | 0 | 0 |

Wrong answers:

- "POS PURCHASE THE WORKS #5261 SEATTLE WA": family by mcc, want shopping
- "KARTENZAHLUNG THE WORKS BERLIN DE": family by mcc, want shopping
- "POS PURCHASE WARHAMMER #8931 BOSTON WA": family by mcc, want leisure
- "KARTENZAHLUNG WARHAMMER HAMBURG DE": family by mcc, want leisure
