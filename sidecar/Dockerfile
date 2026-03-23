FROM python:3.11-slim-bookworm

# Firefox / Camoufox runtime dependencies + Xvfb for virtual display
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    xvfb \
    xauth \
    libgtk-3-0 \
    libdbus-glib-1-2 \
    libxt6 \
    libasound2 \
    libx11-xcb1 \
    libxcomposite1 \
    libxdamage1 \
    libxrandr2 \
    libxcursor1 \
    libxi6 \
    libpango-1.0-0 \
    libpangocairo-1.0-0 \
    libcairo2 \
    libatk1.0-0 \
    libgdk-pixbuf-2.0-0 \
    libnspr4 \
    libnss3 \
    libxss1 \
    fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Download the custom Camoufox Firefox binary
RUN python -m camoufox fetch

COPY app.py .

EXPOSE 8191

HEALTHCHECK --interval=10s --timeout=5s --retries=5 --start-period=60s \
    CMD curl -sf http://localhost:8191/health || exit 1

# Camoufox runs headed (better fingerprints); Xvfb provides virtual display.
# Use shell form so xvfb-run can wrap the process properly.
CMD xvfb-run --auto-servernum --server-args="-screen 0 1280x720x24" \
    uvicorn app:app --host 0.0.0.0 --port 8191 --workers 1
