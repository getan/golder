$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
try {
    $request = $global:GolderRequest
    $script:inputAttempted = $false
    Add-Type -AssemblyName UIAutomationClient
    Add-Type -AssemblyName UIAutomationTypes
    Add-Type -AssemblyName System.Drawing
    Add-Type -ReferencedAssemblies 'System.dll','System.Drawing.dll' -TypeDefinition @'
using System;
using System.Drawing;
using System.Drawing.Imaging;
using System.IO;
using System.Runtime.InteropServices;
public static class GolderWindowCheck {
    [DllImport("user32.dll")] public static extern bool IsWindow(IntPtr window);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr window);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr window);
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr window, out uint pid);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr window, out Rect rect);
    [DllImport("dwmapi.dll")] public static extern int DwmGetWindowAttribute(IntPtr window, int attribute, out Rect rect, int size);
    [DllImport("kernel32.dll", SetLastError=true)] public static extern bool SetDefaultDllDirectories(uint flags);
    [DllImport("user32.dll")] public static extern bool GetWindowDisplayAffinity(IntPtr window, out uint affinity);
    [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr window, IntPtr dc, uint flags);
    [DllImport("user32.dll")] public static extern IntPtr SetThreadDpiAwarenessContext(IntPtr context);
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern IntPtr GetTopWindow(IntPtr window);
    [DllImport("user32.dll")] public static extern IntPtr GetWindow(IntPtr window, uint command);
    [DllImport("user32.dll")] public static extern IntPtr GetAncestor(IntPtr window, uint flags);
    [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(Point point);
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] public static extern bool GetCursorPos(out Point point);
    [DllImport("user32.dll")] public static extern short GetAsyncKeyState(int key);
    [DllImport("user32.dll")] public static extern bool GetGUIThreadInfo(uint thread, ref GuiThreadInfo info);
    [DllImport("user32.dll", SetLastError=true)] public static extern uint SendInput(uint count, Input[] input, int size);
    [StructLayout(LayoutKind.Sequential)] public struct Rect { public int left, top, right, bottom; }
    [StructLayout(LayoutKind.Sequential)] public struct Point { public int x, y; }
    [StructLayout(LayoutKind.Sequential)] public struct GuiThreadInfo {
        public uint size, flags;
        public IntPtr active, focus, capture, menuOwner, moveSize, caret;
        public Rect caretRect;
    }
    [StructLayout(LayoutKind.Sequential)] public struct MouseInput { public int x, y; public uint data, flags, time; public UIntPtr extra; }
    [StructLayout(LayoutKind.Sequential)] public struct KeyInput { public ushort key, scan; public uint flags, time; public UIntPtr extra; }
    [StructLayout(LayoutKind.Explicit)] public struct InputUnion {
        [FieldOffset(0)] public MouseInput mouse;
        [FieldOffset(0)] public KeyInput keyboard;
    }
    [StructLayout(LayoutKind.Sequential)] public struct Input { public uint type; public InputUnion data; }
    public static bool DpiAware() { return SetThreadDpiAwarenessContext(new IntPtr(-4)) != IntPtr.Zero; }
    public static bool GetRect(IntPtr window, bool extended, out Rect rect) {
        if (extended) return DwmGetWindowAttribute(window, 9, out rect, Marshal.SizeOf(typeof(Rect))) == 0;
        return GetWindowRect(window, out rect);
    }
    public static bool SameRect(IntPtr window, int[] expected, bool extended) {
        Rect rect;
        return expected != null && expected.Length == 4 && GetRect(window, extended, out rect) &&
            rect.left == expected[0] && rect.top == expected[1] && rect.right == expected[2] && rect.bottom == expected[3];
    }
    public static bool SameProcess(IntPtr window, uint expectedPid, ulong expectedStart) {
        uint pid;
        if (GetWindowThreadProcessId(window, out pid) == 0 || pid != expectedPid) return false;
        try {
            using (var process = System.Diagnostics.Process.GetProcessById((int)pid))
                return (ulong)process.StartTime.ToFileTimeUtc() == expectedStart;
        } catch { return false; }
    }
    public static bool NoHeldInput() {
        for (int key = 1; key < 256; key++)
            if ((GetAsyncKeyState(key) & 0x8000) != 0) return false;
        return true;
    }
    public static bool SafeInputState(IntPtr target, uint expectedPid, bool keyboard) {
        var info = new GuiThreadInfo { size=(uint)Marshal.SizeOf(typeof(GuiThreadInfo)) };
        if (!GetGUIThreadInfo(0, ref info) || info.capture != IntPtr.Zero || info.menuOwner != IntPtr.Zero || info.moveSize != IntPtr.Zero) return false;
        if (!keyboard) return true;
        uint owner;
        return info.focus != IntPtr.Zero && GetWindowThreadProcessId(info.focus, out owner) != 0 && owner == expectedPid && GetAncestor(info.focus, 2) == target;
    }
    public static bool SafePoint(IntPtr target, uint expectedPid, int x, int y) {
        if (GetForegroundWindow() != target) return false;
        var point = new Point { x=x, y=y };
        var hit = WindowFromPoint(point);
        uint owner;
        if (hit == IntPtr.Zero || GetWindowThreadProcessId(hit, out owner) == 0 || owner != expectedPid || GetAncestor(hit, 2) != target) return false;
        var current = GetTopWindow(IntPtr.Zero);
        for (int i = 0; current != IntPtr.Zero && i < 1024; i++, current = GetWindow(current, 2)) {
            if (current == target) return true;
            Rect rect;
            if (IsWindowVisible(current) && !IsIconic(current) && GetWindowRect(current, out rect) &&
                x >= rect.left && x < rect.right && y >= rect.top && y < rect.bottom) return false;
        }
        return false;
    }
    public static bool ReadyPoint(IntPtr target, uint pid, ulong start, int[] rect, bool extended, int x, int y) {
        return NoHeldInput() && SafeInputState(target, pid, false) && SameProcess(target, pid, start) && SameRect(target, rect, extended) && SafePoint(target, pid, x, y);
    }
    public static bool SendPair(Input[] input) {
        if (input == null || input.Length != 2) return false;
        uint sent = SendInput(2, input, Marshal.SizeOf(typeof(Input)));
        if (sent == 1) SendInput(1, new Input[] { input[1] }, Marshal.SizeOf(typeof(Input)));
        return sent == 2;
    }
    public static bool PreparePoint(IntPtr target, uint pid, ulong start, int[] rect, bool extended, int x, int y) {
        if (!ReadyPoint(target, pid, start, rect, extended, x, y) || !SetCursorPos(x, y)) return false;
        Point actual;
        return GetCursorPos(out actual) && actual.x == x && actual.y == y && ReadyPoint(target, pid, start, rect, extended, x, y);
    }
    public static bool PointInput(IntPtr target, uint pid, ulong start, int[] rect, bool extended, int x, int y, bool click, int dx, int dy) {
        if ((click && (dx != 0 || dy != 0)) || (!click && ((dx == 0 && dy == 0) || (dx != 0 && dy != 0)))) return false;
        Point actual;
        if (!GetCursorPos(out actual) || actual.x != x || actual.y != y || !ReadyPoint(target, pid, start, rect, extended, x, y)) return false;
        if (click) {
            return SendPair(new Input[] {
                new Input { type=0, data=new InputUnion { mouse=new MouseInput { flags=0x0002 } } },
                new Input { type=0, data=new InputUnion { mouse=new MouseInput { flags=0x0004 } } },
            });
        }
        if (dx != 0) {
            var horizontal = new Input[] { new Input { type=0, data=new InputUnion { mouse=new MouseInput { flags=0x1000, data=unchecked((uint)(dx*120)) } } } };
            if (SendInput(1, horizontal, Marshal.SizeOf(typeof(Input))) != 1) return false;
        }
        if (dy != 0) {
            if (!GetCursorPos(out actual) || actual.x != x || actual.y != y || !ReadyPoint(target, pid, start, rect, extended, x, y)) return false;
            var vertical = new Input[] { new Input { type=0, data=new InputUnion { mouse=new MouseInput { flags=0x0800, data=unchecked((uint)(dy*120)) } } } };
            if (SendInput(1, vertical, Marshal.SizeOf(typeof(Input))) != 1) return false;
        }
        return dx != 0 || dy != 0;
    }
    public static bool Key(IntPtr target, uint pid, ulong start, int[] rect, bool extendedRect, ushort key, bool extendedKey) {
        if (!NoHeldInput() || !SafeInputState(target, pid, true) || !SameProcess(target, pid, start) || GetForegroundWindow() != target || !SameRect(target, rect, extendedRect)) return false;
        uint flags = extendedKey ? 1u : 0u;
        var inputs = new Input[] {
            new Input { type=1, data=new InputUnion { keyboard=new KeyInput { key=key, flags=flags } } },
            new Input { type=1, data=new InputUnion { keyboard=new KeyInput { key=key, flags=flags | 2u } } },
        };
        if (!NoHeldInput() || !SafeInputState(target, pid, true) || !SameProcess(target, pid, start) || GetForegroundWindow() != target || !SameRect(target, rect, extendedRect)) return false;
        return SendPair(inputs);
    }
    public static byte[] Capture(IntPtr window) {
        Rect rect;
        uint affinity;
        if (!GetWindowRect(window, out rect) || !GetWindowDisplayAffinity(window, out affinity) || affinity != 0) throw new InvalidOperationException();
        int width = rect.right - rect.left, height = rect.bottom - rect.top;
        if (width <= 0 || height <= 0 || (long)width * height > 16777216) throw new InvalidOperationException();
        using (var bitmap = new Bitmap(width, height, PixelFormat.Format32bppRgb))
        using (var graphics = Graphics.FromImage(bitmap))
        using (var stream = new MemoryStream()) {
            IntPtr dc = graphics.GetHdc();
            try { if (!PrintWindow(window, dc, 2)) throw new InvalidOperationException(); }
            finally { graphics.ReleaseHdc(dc); }
            bitmap.Save(stream, ImageFormat.Png);
            if (stream.Length == 0 || stream.Length > 8388608) throw new InvalidOperationException();
            return stream.ToArray();
        }
    }
}
'@
    $hwnd = [IntPtr]::new([long]$request.hwnd)
    function Test-Target {
        $owner = [uint32]0
        if (-not [GolderWindowCheck]::IsWindow($hwnd) -or
            -not [GolderWindowCheck]::IsWindowVisible($hwnd) -or
            [GolderWindowCheck]::IsIconic($hwnd)) { return $false }
        if ([GolderWindowCheck]::GetWindowThreadProcessId($hwnd, [ref]$owner) -eq 0 -or $owner -ne [uint32]$request.pid) { return $false }
        $process = [System.Diagnostics.Process]::GetProcessById([int]$owner)
        try { return ([uint64]$process.StartTime.ToFileTimeUtc() -eq [uint64]$request.start) }
        finally { $process.Dispose() }
    }
    if (-not (Test-Target)) { throw 'target lost' }
    if ($request.op -eq 'capture') {
        if (-not [GolderWindowCheck]::DpiAware()) { throw 'DPI awareness unavailable' }
        $affinity = [uint32]0
        if ([GolderWindowCheck]::GetWindowDisplayAffinity($hwnd, [ref]$affinity) -and $affinity -ne 0) { throw 'protected window' }
        $rect = [GolderWindowCheck+Rect]::new()
        $kind = 'extended'
        try {
            if (-not [GolderWindowCheck]::GetRect($hwnd, $true, [ref]$rect)) { throw 'window bounds unavailable' }
            $bounds = [int[]]@($rect.left, $rect.top, $rect.right, $rect.bottom)
            Add-Type -AssemblyName System.Runtime.WindowsRuntime
            $metadata = Join-Path ([Environment]::SystemDirectory) 'WinMetadata'
            $refs = @('System.dll', 'System.Core.dll', [System.Runtime.InteropServices.WindowsRuntime.WindowsRuntimeMarshal].Assembly.Location)
            $refs += @(Get-ChildItem -LiteralPath $metadata -Filter '*.winmd' -File | ForEach-Object { $_.FullName })
            $facade = Join-Path ([System.Runtime.InteropServices.RuntimeEnvironment]::GetRuntimeDirectory()) 'Facades\System.Runtime.dll'
            if (Test-Path -LiteralPath $facade) { $refs += $facade }
            if ($refs.Count -lt 4 -or -not [GolderWindowCheck]::SetDefaultDllDirectories(0x800)) { throw 'WinRT metadata unavailable' }
            $wgcSource = [System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($global:GolderWGC))
            Add-Type -ReferencedAssemblies $refs -TypeDefinition $wgcSource
            $png = [GolderWindowsGraphicsCapture]::Capture($hwnd)
            $stream = [System.IO.MemoryStream]::new($png, $false)
            try {
                $image = [System.Drawing.Image]::FromStream($stream)
                try {
                    if ($image.Width -ne ($bounds[2] - $bounds[0]) -or $image.Height -ne ($bounds[3] - $bounds[1])) {
                        $kind = 'window'
                        if (-not [GolderWindowCheck]::GetRect($hwnd, $false, [ref]$rect)) { throw 'window bounds unavailable' }
                        $bounds = [int[]]@($rect.left, $rect.top, $rect.right, $rect.bottom)
                        if ($image.Width -ne ($bounds[2] - $bounds[0]) -or $image.Height -ne ($bounds[3] - $bounds[1])) { throw 'window geometry mismatch' }
                    }
                } finally { $image.Dispose() }
            } finally { $stream.Dispose() }
        } catch {
            $kind = 'window'
            if (-not [GolderWindowCheck]::GetRect($hwnd, $false, [ref]$rect)) { throw 'target lost' }
            $bounds = [int[]]@($rect.left, $rect.top, $rect.right, $rect.bottom)
            $png = [GolderWindowCheck]::Capture($hwnd)
        }
        if (-not (Test-Target) -or -not [GolderWindowCheck]::SameRect($hwnd, $bounds, ($kind -eq 'extended'))) { throw 'target lost' }
        [Console]::Out.Write((@{ png=[Convert]::ToBase64String($png); rect=$bounds; geometry=$kind } | ConvertTo-Json -Compress))
        exit 0
    }
    $root = [System.Windows.Automation.AutomationElement]::FromHandle($hwnd)
    if ($null -eq $root -or $root.Current.ProcessId -ne [int]$request.pid) { throw 'target lost' }
    function Get-RuntimeID($node) {
        $runtime = $node.GetRuntimeId()
        if ($null -eq $runtime -or $runtime.Length -eq 0) { return '' }
        return (($runtime | ForEach-Object { $_.ToString([System.Globalization.CultureInfo]::InvariantCulture) }) -join ',')
    }
    $rootID = Get-RuntimeID $root
    if ($rootID -eq '' -or (($request.op -eq 'click' -or $request.op -eq 'fill') -and $rootID -ne [string]$request.root)) { throw 'target lost' }
    $walker = [System.Windows.Automation.TreeWalker]::ControlViewWalker
    function Test-InputElement($candidate, [string]$expectedID) {
        if ($null -eq $candidate) { return $false }
        $id = Get-RuntimeID $candidate
        if ($id -eq '' -or ($expectedID -ne '' -and $id -ne $expectedID)) { return $false }
        for ($depth = 0; $null -ne $candidate -and $depth -lt 64; $depth++) {
            $current = $candidate.Current
            if ($current.ProcessId -ne [int]$request.pid -or $current.IsPassword) { return $false }
            if ((Get-RuntimeID $candidate) -eq $rootID) { return $true }
            $candidate = $walker.GetParent($candidate)
        }
        return $false
    }
    if ($request.op -eq 'point-click' -or $request.op -eq 'point-scroll' -or $request.op -eq 'key') {
        if ($request.geometry -ne 'window' -and $request.geometry -ne 'extended') { throw 'invalid geometry' }
        $extendedRect = $request.geometry -eq 'extended'
        if (-not [GolderWindowCheck]::DpiAware() -or -not [GolderWindowCheck]::SameRect($hwnd, [int[]]$request.rect, $extendedRect) -or [GolderWindowCheck]::GetForegroundWindow() -ne $hwnd) { throw 'target lost' }
        if ($request.op -eq 'key') {
            $element = [System.Windows.Automation.AutomationElement]::FocusedElement
        } else {
            if ([double]$request.x -lt 0 -or [double]$request.x -ge 1 -or [double]$request.y -lt 0 -or [double]$request.y -ge 1) { throw 'invalid point' }
            $x = [int]([math]::Floor($request.rect[0] + [double]$request.x * ($request.rect[2] - $request.rect[0])))
            $y = [int]([math]::Floor($request.rect[1] + [double]$request.y * ($request.rect[3] - $request.rect[1])))
            if (-not [GolderWindowCheck]::SafePoint($hwnd, [uint32]$request.pid, $x, $y)) { throw 'unsafe point' }
            $element = [System.Windows.Automation.AutomationElement]::FromPoint([System.Windows.Point]::new($x, $y))
        }
        if ($null -eq $element) { throw 'target lost' }
        $elementID = Get-RuntimeID $element
        if ($elementID -eq '' -or -not (Test-InputElement $element $elementID) -or -not (Test-Target)) { throw 'target lost' }
        if ($request.op -eq 'key') {
            $keys = @{ enter=13; tab=9; escape=27; backspace=8; delete=46; up=38; down=40; left=37; right=39; page_up=33; page_down=34; home=36; end=35; space=32 }
            if (-not $keys.ContainsKey([string]$request.key)) { throw 'unsupported key' }
            $extended = @('delete','up','down','left','right','page_up','page_down','home','end') -contains [string]$request.key
            $focused = [System.Windows.Automation.AutomationElement]::FocusedElement
            if (-not (Test-InputElement $focused $elementID) -or -not (Test-Target)) { throw 'focus changed' }
            $script:inputAttempted = $true
            if (-not [GolderWindowCheck]::Key($hwnd, [uint32]$request.pid, [uint64]$request.start, [int[]]$request.rect, $extendedRect, [uint16]$keys[[string]$request.key], $extended)) { throw 'input refused' }
        } else {
            if ([int]$request.dx -lt -5 -or [int]$request.dx -gt 5 -or [int]$request.dy -lt -5 -or [int]$request.dy -gt 5) { throw 'invalid scroll' }
            if ($request.op -eq 'point-scroll' -and [int]$request.dx -eq 0 -and [int]$request.dy -eq 0) { throw 'invalid scroll' }
            $script:inputAttempted = $true
            if (-not [GolderWindowCheck]::PreparePoint($hwnd, [uint32]$request.pid, [uint64]$request.start, [int[]]$request.rect, $extendedRect, $x, $y)) { throw 'input refused' }
            $hit = [System.Windows.Automation.AutomationElement]::FromPoint([System.Windows.Point]::new($x, $y))
            if (-not (Test-InputElement $hit $elementID) -or -not (Test-Target)) { throw 'point changed' }
            if ($request.op -eq 'point-click') {
                if (-not [GolderWindowCheck]::PointInput($hwnd, [uint32]$request.pid, [uint64]$request.start, [int[]]$request.rect, $extendedRect, $x, $y, $true, 0, 0)) { throw 'input refused' }
            } else {
                if ([int]$request.dx -ne 0 -and -not [GolderWindowCheck]::PointInput($hwnd, [uint32]$request.pid, [uint64]$request.start, [int[]]$request.rect, $extendedRect, $x, $y, $false, [int]$request.dx, 0)) { throw 'input refused' }
                if ([int]$request.dy -ne 0) {
                    if ([int]$request.dx -ne 0) {
                        $hit = [System.Windows.Automation.AutomationElement]::FromPoint([System.Windows.Point]::new($x, $y))
                        if (-not (Test-InputElement $hit '') -or -not (Test-Target)) { throw 'scroll may be incomplete' }
                    }
                    if (-not [GolderWindowCheck]::PointInput($hwnd, [uint32]$request.pid, [uint64]$request.start, [int[]]$request.rect, $extendedRect, $x, $y, $false, 0, [int]$request.dy)) { throw 'scroll may be incomplete' }
                }
            }
        }
        [Console]::Out.Write('{"ok":true}')
        exit 0
    }
    $script:nodes = [System.Collections.ArrayList]::new()
    $script:wanted = $null
    $script:matches = 0
    $script:visited = 0
    $script:truncated = $false
    function Visit-Element($node, [int]$depth) {
        if ($null -eq $node) { return }
        if ($depth -gt 16 -or $script:visited -ge 500 -or $script:nodes.Count -ge 128) {
            $script:truncated = $true
            return
        }
        $script:visited++
        try {
            $current = $node.Current
            if ($current.ProcessId -ne [int]$request.pid) { return }
            $id = Get-RuntimeID $node
            if ($id -eq '') { return }
            $secure = [bool]$current.IsPassword
            $pattern = $null
            $editable = $false
            if (-not $secure -and $node.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern, [ref]$pattern)) {
                $editable = -not [bool]$pattern.Current.IsReadOnly
            }
            $name = ''
            if (-not $secure -and -not $editable) { $name = [string]$current.Name }
            if ($name.Length -gt 512) { $name = $name.Substring(0, 512) }
            $role = [string]$current.ControlType.ProgrammaticName
            $enabled = [bool]$current.IsEnabled -and -not [bool]$current.IsOffscreen
            [void]$script:nodes.Add(@{ id=$id; role=$role; name=$name; secure=$secure; editable=$editable; enabled=$enabled })
            if (-not $secure -and $id -eq [string]$request.ref) {
                $script:wanted = $node
                $script:matches++
            }
            if ($secure) { return }
            $child = $walker.GetFirstChild($node)
            while ($null -ne $child -and $script:visited -lt 500 -and $script:nodes.Count -lt 128) {
                Visit-Element $child ($depth + 1)
                $child = $walker.GetNextSibling($child)
            }
            if ($null -ne $child) { $script:truncated = $true }
        } catch { $script:truncated = $true; return }
    }
    Visit-Element $root 0
    if ($request.op -eq 'snapshot') {
        if (-not (Test-Target)) { throw 'target lost' }
        $result = @{ nodes=@($script:nodes.ToArray()); truncated=$script:truncated; root=$rootID }
        [Console]::Out.Write(($result | ConvertTo-Json -Compress -Depth 8))
    } elseif ($request.op -eq 'click' -or $request.op -eq 'fill') {
        if ($null -eq $script:wanted -or $script:matches -ne 1 -or $script:truncated -or -not (Test-Target)) { throw 'target lost' }
        $current = $script:wanted.Current
        if ($current.ProcessId -ne [int]$request.pid -or $current.IsPassword -or -not $current.IsEnabled -or $current.IsOffscreen) { throw 'target unavailable' }
        $pattern = $null
        if ($request.op -eq 'click') {
            if (-not $script:wanted.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) { throw 'action unavailable' }
            if (-not (Test-Target)) { throw 'target lost' }
            $script:inputAttempted = $true
            $pattern.Invoke()
        } else {
            if (-not $script:wanted.TryGetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern, [ref]$pattern) -or $pattern.Current.IsReadOnly) { throw 'action unavailable' }
            if (-not (Test-Target)) { throw 'target lost' }
            $script:inputAttempted = $true
            $pattern.SetValue([string]$request.text)
        }
        [Console]::Out.Write('{"ok":true}')
    } else { throw 'unsupported operation' }
} catch {
    if ($request.op -eq 'capture') {
        [Console]::Error.Write('GOLDER_CAPTURE_UNAVAILABLE')
    } elseif ($script:inputAttempted) {
        [Console]::Error.Write('GOLDER_INPUT_PARTIAL')
    } else {
        [Console]::Error.Write('GOLDER_UIA_UNAVAILABLE')
    }
    exit 1
}
