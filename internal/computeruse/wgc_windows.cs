using System;
using System.Runtime.InteropServices;
using System.Runtime.InteropServices.WindowsRuntime;
using System.Threading;
using System.Threading.Tasks;
using Windows.Graphics.Capture;
using Windows.Graphics.DirectX;
using Windows.Graphics.DirectX.Direct3D11;
using Windows.Graphics.Imaging;
using Windows.Storage.Streams;

// Compiled only in the isolated Windows helper, using system WinRT metadata.
// It captures an HWND, never a monitor or the desktop.
public static class GolderWindowsGraphicsCapture {
    [ComImport, Guid("3628E81B-3CAC-4C60-B7F4-23CE0E0C3356"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IGraphicsCaptureItemInterop {
        [PreserveSig] int CreateForWindow(IntPtr window, ref Guid iid, out IntPtr result);
        [PreserveSig] int CreateForMonitor(IntPtr monitor, ref Guid iid, out IntPtr result);
    }

    [DllImport("d3d11.dll", ExactSpelling=true)]
    private static extern int D3D11CreateDevice(IntPtr adapter, int driverType, IntPtr software, uint flags,
        IntPtr featureLevels, uint featureCount, uint sdkVersion, out IntPtr device, out uint featureLevel, out IntPtr context);

    [DllImport("d3d11.dll", ExactSpelling=true)]
    private static extern int CreateDirect3D11DeviceFromDXGIDevice(IntPtr dxgi, out IntPtr inspectable);

    [DllImport("combase.dll", ExactSpelling=true)]
    private static extern int RoInitialize(uint initType);

    [DllImport("combase.dll", ExactSpelling=true)]
    private static extern void RoUninitialize();

    private static void Check(int hr) {
        if (hr < 0) Marshal.ThrowExceptionForHR(hr);
    }

    public static byte[] Capture(IntPtr window) {
        int hr = RoInitialize(0); // STA, or the apartment already chosen by PowerShell
        if (unchecked((uint)hr) == 0x80010106) hr = RoInitialize(1);
        Check(hr);
        try {
            if (!GraphicsCaptureSession.IsSupported()) throw new InvalidOperationException();
            return CaptureAsync(window).GetAwaiter().GetResult();
        } finally {
            RoUninitialize();
        }
    }

    private static async Task<byte[]> CaptureAsync(IntPtr window) {
        IntPtr nativeDevice = IntPtr.Zero, nativeContext = IntPtr.Zero, dxgi = IntPtr.Zero, wrapped = IntPtr.Zero, itemPointer = IntPtr.Zero;
        try {
            uint level;
            int hr = D3D11CreateDevice(IntPtr.Zero, 1, IntPtr.Zero, 0x20, IntPtr.Zero, 0, 7, out nativeDevice, out level, out nativeContext);
            if (hr < 0) {
                if (nativeContext != IntPtr.Zero) Marshal.Release(nativeContext);
                if (nativeDevice != IntPtr.Zero) Marshal.Release(nativeDevice);
                nativeDevice = nativeContext = IntPtr.Zero;
                hr = D3D11CreateDevice(IntPtr.Zero, 5, IntPtr.Zero, 0x20, IntPtr.Zero, 0, 7, out nativeDevice, out level, out nativeContext);
            }
            Check(hr);
            Guid dxgiID = new Guid("54EC77FA-1377-44E6-8C32-88FD5F44C84C");
            Check(Marshal.QueryInterface(nativeDevice, ref dxgiID, out dxgi));
            Check(CreateDirect3D11DeviceFromDXGIDevice(dxgi, out wrapped));
            var device = Marshal.GetObjectForIUnknown(wrapped) as IDirect3DDevice;
            if (device == null) throw new InvalidOperationException();
            var factory = WindowsRuntimeMarshal.GetActivationFactory(typeof(GraphicsCaptureItem)) as IGraphicsCaptureItemInterop;
            if (factory == null) throw new InvalidOperationException();
            Guid itemID = new Guid("79C3F95B-31F7-4EC2-A464-632EF5D30760");
            Check(factory.CreateForWindow(window, ref itemID, out itemPointer));
            var item = Marshal.GetObjectForIUnknown(itemPointer) as GraphicsCaptureItem;
            if (item == null) throw new InvalidOperationException();
            int width = item.Size.Width, height = item.Size.Height;
            if (width <= 0 || height <= 0 || (long)width * height > 16777216) throw new InvalidOperationException();
            using (var pool = Direct3D11CaptureFramePool.CreateFreeThreaded(device, DirectXPixelFormat.B8G8R8A8UIntNormalized, 1, item.Size))
            using (var capture = pool.CreateCaptureSession(item))
            using (var ready = new ManualResetEventSlim(false)) {
                Windows.Foundation.TypedEventHandler<Direct3D11CaptureFramePool, object> arrived = delegate { try { ready.Set(); } catch (ObjectDisposedException) { } };
                pool.FrameArrived += arrived;
                try {
                    capture.StartCapture();
                    if (!ready.Wait(TimeSpan.FromSeconds(4))) throw new TimeoutException();
                    using (var frame = pool.TryGetNextFrame()) {
                        if (frame == null || frame.ContentSize.Width != width || frame.ContentSize.Height != height) throw new InvalidOperationException();
                        using (var bitmap = await SoftwareBitmap.CreateCopyFromSurfaceAsync(frame.Surface).AsTask().ConfigureAwait(false))
                        using (var output = new InMemoryRandomAccessStream()) {
                            if (bitmap.PixelWidth != width || bitmap.PixelHeight != height) throw new InvalidOperationException();
                            var encoder = await BitmapEncoder.CreateAsync(BitmapEncoder.PngEncoderId, output).AsTask().ConfigureAwait(false);
                            encoder.SetSoftwareBitmap(bitmap);
                            await encoder.FlushAsync().AsTask().ConfigureAwait(false);
                            if (output.Size == 0 || output.Size > 8388608) throw new InvalidOperationException();
                            output.Seek(0);
                            using (var reader = new DataReader(output.GetInputStreamAt(0))) {
                                uint size = (uint)output.Size;
                                if (await reader.LoadAsync(size).AsTask().ConfigureAwait(false) != size) throw new InvalidOperationException();
                                byte[] data = new byte[(int)size];
                                reader.ReadBytes(data);
                                return data;
                            }
                        }
                    }
                } finally {
                    pool.FrameArrived -= arrived;
                }
            }
        } finally {
            if (itemPointer != IntPtr.Zero) Marshal.Release(itemPointer);
            if (wrapped != IntPtr.Zero) Marshal.Release(wrapped);
            if (dxgi != IntPtr.Zero) Marshal.Release(dxgi);
            if (nativeContext != IntPtr.Zero) Marshal.Release(nativeContext);
            if (nativeDevice != IntPtr.Zero) Marshal.Release(nativeDevice);
        }
    }
}
