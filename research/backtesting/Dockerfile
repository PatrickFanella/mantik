FROM python:3.13.9-slim-bookworm@sha256:b685a4fa58bb19d1814d78a1ec0f0208f351452724f78b20212c984d6e124a34
WORKDIR /adapter
COPY requirements.lock .
RUN python -m pip install --no-cache-dir --only-binary=:all: --require-hashes -r requirements.lock
COPY adapter.py test_adapter.py ./
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 OPENBLAS_NUM_THREADS=1 OMP_NUM_THREADS=1
USER 65532:65532
ENTRYPOINT ["python", "-I", "/adapter/adapter.py"]
