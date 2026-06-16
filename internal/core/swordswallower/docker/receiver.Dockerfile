FROM python:3.12-alpine

WORKDIR /app
COPY tools/receiver.py /app/receiver.py

EXPOSE 8787
CMD ["python", "/app/receiver.py"]
