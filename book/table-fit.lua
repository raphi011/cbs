-- Give width-less tables proportional column widths so the LaTeX/PDF build uses
-- wrapping `p{}` columns instead of non-wrapping `l` columns (which clip wide
-- cells off the page). Widths are proportional to each column's longest cell, so
-- a long "Purpose" column gets the room it needs and short columns stay narrow.
-- Tables that already carry widths (e.g. grid tables) are left untouched.

local stringify = pandoc.utils.stringify

local function column_maxwidths(tbl)
  local ncol = #tbl.colspecs
  local w = {}
  for i = 1, ncol do w[i] = 1 end
  local function scan(rows)
    for _, row in ipairs(rows) do
      for i, cell in ipairs(row.cells) do
        local len = #stringify(cell.contents)
        if i <= ncol and len > w[i] then w[i] = len end
      end
    end
  end
  scan(tbl.head.rows)
  for _, body in ipairs(tbl.bodies) do
    scan(body.head)
    scan(body.body)
  end
  return w, ncol
end

function Table(tbl)
  for _, cs in ipairs(tbl.colspecs) do
    if cs[2] ~= nil and cs[2] > 0 then return nil end -- widths already set
  end

  local w, ncol = column_maxwidths(tbl)
  local total = 0
  for i = 1, ncol do
    if w[i] < 4 then w[i] = 4 end   -- floor so a tiny column isn't razor-thin
    if w[i] > 48 then w[i] = 48 end -- cap so one long column can't starve the rest
    total = total + w[i]
  end
  for i, cs in ipairs(tbl.colspecs) do
    cs[2] = w[i] / total
  end
  return tbl
end
