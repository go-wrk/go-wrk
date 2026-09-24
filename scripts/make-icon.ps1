# 从 image/ 里的图生成应用图标。
#
#   .\scripts\make-icon.ps1              # 用 image\ 里最新的那张图
#   .\scripts\make-icon.ps1 别的图.png    # 指定某张图
#
# 生成两样东西：
#   build\appicon.png       1024x1024 的源图
#   build\windows\icon.ico  16/32/48/64/128/256 六个尺寸
#
# 为什么要手动跑：Wails 构建时并不会从 appicon.png 生成 icon.ico，
# 不跑这个脚本的话，窗口和任务栏图标还是模板自带的那个。
#
# 图不是正方形也能用，脚本会补边：原图带透明通道就补透明边，
# 不透明就取左上角的颜色来补。内容等比缩放居中。

param(
    [string]$Source
)

$ErrorActionPreference = "Stop"
Add-Type -AssemblyName System.Drawing

# $PSScriptRoot 只有用 -File 跑脚本时才有值，从别处调用就退回当前目录
$root = if ($PSScriptRoot) { Split-Path -Parent $PSScriptRoot } else { (Get-Location).Path }

# 没指定就用 image\ 里最近改过的那张。
# 只看顶层、不进子目录 —— 截图放在 image\screenshots\ 下，别把界面截图做成图标。
if (-not $Source) {
    $imgDir = Join-Path $root "image"
    if (-not (Test-Path $imgDir)) { throw "找不到 image\ 目录" }
    $candidates = Get-ChildItem $imgDir -File |
        Where-Object { $_.Extension -match '^\.(png|jpg|jpeg|bmp)$' } |
        Sort-Object LastWriteTime -Descending
    if (-not $candidates) { throw "image\ 里没有图片（png/jpg/bmp）" }
    $Source = $candidates[0].FullName
    Write-Host "用 image\ 里最新的图: $($candidates[0].Name)"
}

if (-not (Test-Path $Source)) { throw "找不到文件: $Source" }
$Source = (Resolve-Path $Source).Path

$outPng = Join-Path $root "build\appicon.png"
$outIco = Join-Path $root "build\windows\icon.ico"
foreach ($p in @($outPng, $outIco)) {
    $dir = Split-Path -Parent $p
    if (-not (Test-Path $dir)) { throw "找不到输出目录: $dir" }
}

$src = [System.Drawing.Image]::FromFile($Source)
Write-Host ("源图 {0}: {1} x {2}" -f (Split-Path -Leaf $Source), $src.Width, $src.Height)
if ($src.Width -lt 512 -or $src.Height -lt 512) {
    Write-Warning "图比 512 小，生成 256 那档图标会糊，建议换张大的"
}

# 补边用什么色：原图有透明通道就补透明，否则取左上角那块颜色
$corner = $src.GetPixel(0, 0)
$fill = if ($corner.A -lt 255) { [System.Drawing.Color]::Transparent } else { $corner }
if ($src.Width -ne $src.Height) {
    Write-Host ("图不是正方形，补边颜色: A={0} R={1} G={2} B={3}" -f $fill.A, $fill.R, $fill.G, $fill.B)
}

# 等比缩放到 size x size 的画布中央
function New-IconBitmap {
    param([System.Drawing.Image]$Img, [int]$Size, [System.Drawing.Color]$Fill)

    $bmp = New-Object System.Drawing.Bitmap($Size, $Size)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
    $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::HighQuality
    $g.CompositingQuality = [System.Drawing.Drawing2D.CompositingQuality]::HighQuality
    $g.Clear($Fill)

    $scale = [Math]::Min($Size / $Img.Width, $Size / $Img.Height)
    $w = [int]($Img.Width * $scale)
    $h = [int]($Img.Height * $scale)
    $g.DrawImage($Img, [int](($Size - $w) / 2), [int](($Size - $h) / 2), $w, $h)
    $g.Dispose()
    return $bmp
}

# appicon.png
$app = New-IconBitmap -Img $src -Size 1024 -Fill $fill
$app.Save($outPng, [System.Drawing.Imaging.ImageFormat]::Png)
$app.Dispose()
Write-Host ("appicon.png: {0} 字节" -f (Get-Item $outPng).Length)

# icon.ico：ICO 里嵌 PNG（Vista 之后都认），一个文件塞多个尺寸
$sizes = @(16, 32, 48, 64, 128, 256)
$pngs = @()
foreach ($s in $sizes) {
    $bmp = New-IconBitmap -Img $src -Size $s -Fill $fill
    $ms = New-Object System.IO.MemoryStream
    $bmp.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
    $pngs += , $ms.ToArray()
    $bmp.Dispose()
    $ms.Dispose()
}
$src.Dispose()

$fs = [System.IO.File]::Create($outIco)
$bw = New-Object System.IO.BinaryWriter($fs)
$bw.Write([UInt16]0)              # 保留位
$bw.Write([UInt16]1)              # 类型：1 = 图标
$bw.Write([UInt16]$sizes.Count)   # 图像数量
$offset = 6 + 16 * $sizes.Count
for ($i = 0; $i -lt $sizes.Count; $i++) {
    $s = $sizes[$i]
    $dim = if ($s -ge 256) { 0 } else { $s }   # 256 在 ICO 里记作 0
    $bw.Write([Byte]$dim)         # 宽
    $bw.Write([Byte]$dim)         # 高
    $bw.Write([Byte]0)            # 调色板数
    $bw.Write([Byte]0)            # 保留位
    $bw.Write([UInt16]1)          # 色彩平面
    $bw.Write([UInt16]32)         # 每像素位数
    $bw.Write([UInt32]$pngs[$i].Length)
    $bw.Write([UInt32]$offset)
    $offset += $pngs[$i].Length
}
foreach ($p in $pngs) { $bw.Write($p) }
$bw.Close()
$fs.Close()

Write-Host ("icon.ico: {0} 字节（{1} 个尺寸）" -f (Get-Item $outIco).Length, $sizes.Count)
Write-Host ""
Write-Host "跑一下 wails build 就能看到新图标了。" -ForegroundColor Green
