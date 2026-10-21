package statements

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"
)

// excelDateFormats are Excel's built-in number formats that show a date.
var excelDateFormats = map[int]bool{14: true, 15: true, 16: true, 17: true, 22: true, 27: true, 30: true, 36: true, 50: true, 57: true}

// ExtractXLSX reads the first sheet with anything on it into a table.
// Cells formatted as dates become ISO dates (2025-07-14) from Excel's
// serial numbers; other cells keep their raw values. A merged cell's
// value fills every cell it covers, so a header label spanning two
// columns names both.
func ExtractXLSX(data []byte) (RawTable, error) {
	file, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return RawTable{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	defer func() { _ = file.Close() }()

	for _, sheet := range file.GetSheetList() {
		records, err := sheetRecords(file, sheet)
		if err != nil {
			return RawTable{}, fmt.Errorf("%w: %w", ErrUnreadable, err)
		}
		if len(records) == 0 {
			continue
		}
		width, count := modalWidth(records)
		if width < 2 || count < 2 {
			continue
		}
		return tableOf(records, headerRow(records), "\t"), nil
	}
	return RawTable{}, ErrEmptyFile
}

// sheetRecords is a sheet's rows, dates as ISO strings, merged ranges
// filled in and blank rows dropped.
func sheetRecords(file *excelize.File, sheet string) ([][]string, error) {
	rows, err := file.GetRows(sheet, excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, err
	}
	for r, row := range rows {
		for c, value := range row {
			if value == "" {
				continue
			}
			cell, err := excelize.CoordinatesToCellName(c+1, r+1)
			if err != nil {
				return nil, err
			}
			if date, ok := excelDate(file, sheet, cell, value); ok {
				rows[r][c] = date
			}
		}
	}

	merged, err := file.GetMergeCells(sheet)
	if err != nil {
		return nil, err
	}
	for _, m := range merged {
		startCol, startRow, err := excelize.CellNameToCoordinates(m.GetStartAxis())
		if err != nil {
			return nil, err
		}
		endCol, endRow, err := excelize.CellNameToCoordinates(m.GetEndAxis())
		if err != nil {
			return nil, err
		}
		for r := startRow - 1; r < endRow && r < len(rows); r++ {
			for len(rows[r]) < endCol {
				rows[r] = append(rows[r], "")
			}
			for c := startCol - 1; c < endCol; c++ {
				rows[r][c] = m.GetCellValue()
			}
		}
	}

	records := make([][]string, 0, len(rows))
	for _, row := range rows {
		if len(nonEmptyTail(row)) > 0 {
			records = append(records, row)
		}
	}
	return records, nil
}

// excelDate turns a cell formatted as a date from its serial number into
// an ISO date.
func excelDate(file *excelize.File, sheet, cell, value string) (string, bool) {
	serial, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return "", false
	}
	styleID, err := file.GetCellStyle(sheet, cell)
	if err != nil || styleID == 0 {
		return "", false
	}
	style, err := file.GetStyle(styleID)
	if err != nil {
		return "", false
	}
	isDate := excelDateFormats[style.NumFmt]
	if custom := style.CustomNumFmt; custom != nil {
		format := strings.ToLower(*custom)
		isDate = strings.Contains(format, "d") && strings.Contains(format, "y")
	}
	if !isDate {
		return "", false
	}
	at, err := excelize.ExcelDateToTime(serial, false)
	if err != nil {
		return "", false
	}
	return at.Format("2006-01-02"), true
}
