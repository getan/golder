// SPDX-License-Identifier: MIT
// This WASI guest receives its only inputs over stdin.
use std::io::{self, Read, Write};
use std::sync::Arc;

use resvg::{tiny_skia, usvg};

const MAX_SVG: usize = 512 * 1024;
const MAX_FONT: usize = 32 * 1024 * 1024;
const MAX_OUTPUT: usize = 8 * 1024 * 1024;

fn read_bytes(input: &mut impl Read, limit: usize) -> Result<Vec<u8>, &'static str> {
    let mut len = [0; 4];
    input.read_exact(&mut len).map_err(|_| "missing length")?;
    let len = u32::from_le_bytes(len) as usize;
    if len == 0 || len > limit {
        return Err("input size exceeds limit");
    }
    let mut data = vec![0; len];
    input.read_exact(&mut data).map_err(|_| "truncated input")?;
    Ok(data)
}

fn run() -> Result<(), Box<dyn std::error::Error>> {
    let mut input = io::stdin().lock();
    let svg = read_bytes(&mut input, MAX_SVG)?;
    let font = read_bytes(&mut input, MAX_FONT)?;
    let mut opt = usvg::Options::default();
    opt.image_href_resolver = usvg::ImageHrefResolver {
        resolve_data: Box::new(|_: &str, _: Arc<Vec<u8>>, _: &usvg::Options| None),
        resolve_string: Box::new(|_: &str, _: &usvg::Options| None),
    };
    opt.fontdb_mut().load_font_data(font);
    let family = opt
        .fontdb_mut()
        .faces()
        .next()
        .and_then(|face| face.families.first().map(|name| name.0.clone()))
        .ok_or("no usable font")?;
    opt.font_family = family.clone();
    opt.fontdb_mut().set_serif_family(family.clone());
    opt.fontdb_mut().set_sans_serif_family(family.clone());
    opt.fontdb_mut().set_monospace_family(family);
    let tree = usvg::Tree::from_data(&svg, &opt)?;
    let size = tree.size().to_int_size();
    let (w, h) = (size.width(), size.height());
    if w == 0 || h == 0 || w > 8192 || h > 8192 {
        return Err("SVG dimensions exceed limit".into());
    }
    let scale = 1.0_f32.min(1280.0 / w as f32).min(640.0 / h as f32);
    let render_w = ((w as f32 * scale).ceil() as u32).max(1);
    let render_h = ((h as f32 * scale).ceil() as u32).max(1);
    let mut pixmap = tiny_skia::Pixmap::new(render_w, render_h).ok_or("cannot allocate image")?;
    resvg::render(
        &tree,
        tiny_skia::Transform::from_scale(scale, scale),
        &mut pixmap.as_mut(),
    );
    let png = pixmap.encode_png()?;
    if png.len() > MAX_OUTPUT {
        return Err("PNG exceeds limit".into());
    }
    io::stdout().lock().write_all(&png)?;
    Ok(())
}

fn main() {
    if let Err(err) = run() {
        eprintln!("SVG rendering failed: {err}");
        std::process::exit(1);
    }
}
